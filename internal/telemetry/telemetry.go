// Package telemetry sends a single anonymous ping per day to a
// project-operated endpoint so the maintainers can answer two
// questions: "is anyone running this?" and "in which deployment mode?"
//
// **Defaults are off.** A user must explicitly set
// RELIC_TELEMETRY=true to opt in. Without that, this package is inert.
//
// What we collect (opt-in):
//   - build version + git commit
//   - auth_mode (local | supabase | oidc)
//   - bucketed counts: users / agents / runs (buckets are "0", "1-10",
//     "11-100", "101-1000", "1000+"). No precise counts.
//   - whether the governance worker is enabled
//
// What we never collect:
//   - email addresses, organization names, agent names, run IDs, or
//     anything that could deanonymize a deployment.
//   - any trace contents.
//   - exact counts (we bucket because exact run counts can be a
//     business signal).
//
// The reporter URL defaults to https://telemetry.therelic.dev/ping
// (the one host the maintainers operate) but can be overridden via
// RELIC_TELEMETRY_URL for testing or for users who want to mirror.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultURL  = "https://telemetry.therelic.dev/ping"
	pingTimeout = 5 * time.Second
)

// Reporter sends a single ping at startup and one ping per 24h thereafter.
type Reporter struct {
	pool    *pgxpool.Pool
	logger  *slog.Logger
	url     string
	enabled bool
	build   string
	commit  string
	mode    string
}

// New constructs a Reporter from environment variables. When
// RELIC_TELEMETRY is not "true" / "1" / "yes", the returned reporter
// is a no-op. The bool return value tells the caller whether to log
// the opt-in (or its absence).
func New(pool *pgxpool.Pool, logger *slog.Logger, build, commit, authMode string) (*Reporter, bool) {
	enabled := isOptedIn(os.Getenv("RELIC_TELEMETRY"))
	url := strings.TrimSpace(os.Getenv("RELIC_TELEMETRY_URL"))
	if url == "" {
		url = DefaultURL
	}
	return &Reporter{
		pool:    pool,
		logger:  logger,
		url:     url,
		enabled: enabled,
		build:   build,
		commit:  commit,
		mode:    authMode,
	}, enabled
}

func isOptedIn(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Run sends one ping immediately, then one every 24h until ctx is
// done. Failures log at WARN and are not retried within the window;
// telemetry must not be a critical path.
func (r *Reporter) Run(ctx context.Context) {
	if !r.enabled {
		return
	}
	// Initial ping immediately.
	r.ping(ctx)

	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.ping(ctx)
		}
	}
}

type payload struct {
	Build        string `json:"build"`
	Commit       string `json:"commit"`
	AuthMode     string `json:"auth_mode"`
	UsersBucket  string `json:"users_bucket"`
	AgentsBucket string `json:"agents_bucket"`
	RunsBucket   string `json:"runs_bucket"`
	GovEnabled   bool   `json:"governance_enabled"`
}

func (r *Reporter) ping(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, pingTimeout)
	defer cancel()

	body := payload{
		Build:        r.build,
		Commit:       r.commit,
		AuthMode:     r.mode,
		UsersBucket:  bucket(r.count(ctx, `SELECT count(*) FROM users`)),
		AgentsBucket: bucket(r.count(ctx, `SELECT count(*) FROM agents`)),
		RunsBucket:   bucket(r.count(ctx, `SELECT count(*) FROM runs`)),
		GovEnabled:   isOptedIn(os.Getenv("RELIC_GOVERNANCE_ENABLED")),
	}

	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url, bytes.NewReader(b))
	if err != nil {
		r.logger.Warn("telemetry: build request failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.logger.Warn("telemetry: send failed (this is fine)", "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		r.logger.Warn("telemetry: non-2xx response", "status", resp.StatusCode)
	}
}

func (r *Reporter) count(ctx context.Context, q string) int64 {
	var n int64
	if r.pool == nil {
		return 0
	}
	err := r.pool.QueryRow(ctx, q).Scan(&n)
	if err != nil && !errors.Is(err, context.Canceled) {
		// Don't fail the whole ping for one stat; just return 0 which
		// buckets to "0".
		return 0
	}
	return n
}

func bucket(n int64) string {
	switch {
	case n <= 0:
		return "0"
	case n <= 10:
		return "1-10"
	case n <= 100:
		return "11-100"
	case n <= 1000:
		return "101-1000"
	default:
		return "1000+"
	}
}
