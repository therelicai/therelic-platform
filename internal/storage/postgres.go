package storage

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"
)

type Postgres struct {
	pool    *pgxpool.Pool
	replica *pgxpool.Pool // optional read-only replica; nil = reads go to primary
}

// PoolConfig controls the pgxpool we construct. Each field maps onto a
// MaxConns / MinConns / *Lifetime knob — zero values fall back to
// pgxpool's defaults, which are surprisingly conservative for an API
// server (max 4 conns). Real deployments will want to tune at least
// MaxConns.
type PoolConfig struct {
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// NewPostgres connects with pgxpool's defaults. Existing callers
// preserve their behavior; new callers should prefer NewPostgresWith.
func NewPostgres(ctx context.Context, databaseURL string) (*Postgres, error) {
	return NewPostgresWith(ctx, databaseURL, PoolConfig{})
}

// NewPostgresWith connects with explicit pool tuning. Defaults are
// applied for any zero field. We still Ping before returning so an
// unreachable database fails loudly at boot rather than silently at
// first request.
func NewPostgresWith(ctx context.Context, databaseURL string, pc PoolConfig) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}
	if pc.MaxConns > 0 {
		cfg.MaxConns = pc.MaxConns
	}
	if pc.MinConns > 0 {
		cfg.MinConns = pc.MinConns
	}
	if pc.MaxConnLifetime > 0 {
		cfg.MaxConnLifetime = pc.MaxConnLifetime
	}
	if pc.MaxConnIdleTime > 0 {
		cfg.MaxConnIdleTime = pc.MaxConnIdleTime
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Postgres{pool: pool}, nil
}

func (p *Postgres) Close() {
	p.pool.Close()
}

// Pool exposes the underlying pgxpool for callers that need
// connection-level operations the storage API doesn't surface — e.g.,
// the livefeed hub's dedicated LISTEN connection. Callers MUST NOT
// close the pool; ownership stays with this struct.
func (p *Postgres) Pool() *pgxpool.Pool {
	return p.pool
}

// Ping checks the pool can reach the database. Used by /readyz.
func (p *Postgres) Ping(ctx context.Context) error {
	return p.pool.Ping(ctx)
}

// PoolStat is a snapshot of pgxpool counters. Exposed for /metrics so
// operators can see when the API server is saturating its DB pool.
type PoolStat struct {
	Acquired    int32
	Idle        int32
	Max         int32
	Total       int32
	Constructing int32
}

func (p *Postgres) Stats() PoolStat {
	s := p.pool.Stat()
	return PoolStat{
		Acquired:     s.AcquiredConns(),
		Idle:         s.IdleConns(),
		Max:          s.MaxConns(),
		Total:        s.TotalConns(),
		Constructing: s.ConstructingConns(),
	}
}

// --- Organizations ---

type Organization struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Plan      string    `json:"plan"`
	CreatedAt time.Time `json:"created_at"`
}

func (p *Postgres) CreateOrg(ctx context.Context, name, slug string) (*Organization, error) {
	org := &Organization{}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ($1, $2) RETURNING id, name, slug, plan, created_at`,
		name, slug,
	).Scan(&org.ID, &org.Name, &org.Slug, &org.Plan, &org.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create org: %w", err)
	}
	return org, nil
}

func (p *Postgres) GetOrg(ctx context.Context, id string) (*Organization, error) {
	org := &Organization{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, name, slug, plan, created_at FROM organizations WHERE id = $1`, id,
	).Scan(&org.ID, &org.Name, &org.Slug, &org.Plan, &org.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get org: %w", err)
	}
	return org, nil
}

// --- API Keys ---

type APIKey struct {
	ID        string     `json:"id"`
	OrgID     string     `json:"org_id"`
	KeyPrefix string     `json:"key_prefix"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"created_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
}

// hashAPIKeyPlain returns the legacy SHA-256(plaintext) hash. Kept as a
// validation fallback so keys issued before slice 3 keep working.
func hashAPIKeyPlain(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}

// hashAPIKeyHMAC returns HMAC-SHA256(pepper, plaintext) hex-encoded.
// The pepper comes from RELIC_API_KEY_PEPPER; if it's empty the caller
// must fall back to the plain hash (we never silently weaken to plain
// SHA-256 — that would erase the threat model).
func hashAPIKeyHMAC(plaintext string, pepper []byte) string {
	mac := hmac.New(sha256.New, pepper)
	mac.Write([]byte(plaintext))
	return hex.EncodeToString(mac.Sum(nil))
}

// apiKeyPepper returns the bytes of RELIC_API_KEY_PEPPER. Empty means
// the operator opted out of upgraded hashing — CreateAPIKey will write
// the legacy sha256 algorithm in that case.
func apiKeyPepper() []byte {
	return []byte(os.Getenv("RELIC_API_KEY_PEPPER"))
}

func (p *Postgres) CreateAPIKey(ctx context.Context, orgID, name string) (*APIKey, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, "", fmt.Errorf("generate key: %w", err)
	}
	plaintext := "rk_" + hex.EncodeToString(raw)
	prefix := plaintext[:10]

	var keyHash, algo string
	if pepper := apiKeyPepper(); len(pepper) > 0 {
		keyHash = hashAPIKeyHMAC(plaintext, pepper)
		algo = "hmac_sha256"
	} else {
		keyHash = hashAPIKeyPlain(plaintext)
		algo = "sha256"
	}

	key := &APIKey{}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO api_keys (org_id, key_hash, key_prefix, name, hash_algo) VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, org_id, key_prefix, name, created_at`,
		orgID, keyHash, prefix, name, algo,
	).Scan(&key.ID, &key.OrgID, &key.KeyPrefix, &key.Name, &key.CreatedAt)
	if err != nil {
		return nil, "", fmt.Errorf("create api key: %w", err)
	}
	return key, plaintext, nil
}

// ValidateAPIKey resolves a plaintext token to an org_id. We compute
// both hash variants and look up either, so legacy keys (sha256) keep
// working after RELIC_API_KEY_PEPPER is set and new keys can be
// validated immediately. ANY = the hash_algo column distinguishes them
// at the storage level but ValidateAPIKey treats them as interchangeable.
func (p *Postgres) ValidateAPIKey(ctx context.Context, plaintext string) (orgID string, err error) {
	hashes := []string{hashAPIKeyPlain(plaintext)}
	if pepper := apiKeyPepper(); len(pepper) > 0 {
		hashes = append(hashes, hashAPIKeyHMAC(plaintext, pepper))
	}

	err = p.pool.QueryRow(ctx,
		`SELECT org_id FROM api_keys
		   WHERE key_hash = ANY($1) AND revoked_at IS NULL
		   LIMIT 1`,
		hashes,
	).Scan(&orgID)
	if err == pgx.ErrNoRows {
		return "", fmt.Errorf("invalid or revoked API key")
	}
	if err != nil {
		return "", fmt.Errorf("validate key: %w", err)
	}
	return orgID, nil
}

func (p *Postgres) RevokeAPIKey(ctx context.Context, orgID, keyID string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND org_id = $2`, keyID, orgID,
	)
	return err
}

// --- Runs ---

type Run struct {
	ID             string    `json:"id"`
	OrgID          string    `json:"org_id"`
	AgentName      string    `json:"agent_name"`
	AgentVersion   string    `json:"agent_version"`
	PolicyHash     string    `json:"policy_hash"`
	Environment    string    `json:"environment"`
	StartedAt      time.Time `json:"started_at"`
	DurationMs     *int      `json:"duration_ms,omitempty"`
	ExitCode       *int      `json:"exit_code,omitempty"`
	ActionsTotal   int       `json:"actions_total"`
	ActionsAllowed int       `json:"actions_allowed"`
	ActionsDenied  int       `json:"actions_denied"`
	// StorageKey is the internal S3 path and is omitted from JSON
	// responses so we don't leak the bucket layout to clients.
	StorageKey      string    `json:"-"`
	ExpiresAt       time.Time `json:"expires_at"`
	IntegrityChain  bool      `json:"integrity_chain"`
	// ChainVerified is true only when the platform recomputed the
	// HMAC chain end-to-end against its master secret. IntegrityChain
	// is the client's claim; ChainVerified is the server's proof.
	ChainVerified bool `json:"chain_verified"`
	Truncated     bool `json:"truncated"`
}

// ErrRunAlreadyExists indicates an attempt to re-upload a run that's
// already indexed. Callers should treat this as idempotent and return the
// existing row instead of double-charging or double-counting.
var ErrRunAlreadyExists = errors.New("storage: run already exists")

func (p *Postgres) InsertRun(ctx context.Context, r *Run) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO runs (id, org_id, agent_name, agent_version, policy_hash, environment,
		  started_at, duration_ms, exit_code, actions_total, actions_allowed, actions_denied,
		  storage_key, expires_at, integrity_chain, chain_verified, truncated)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		r.ID, r.OrgID, r.AgentName, r.AgentVersion, r.PolicyHash, r.Environment,
		r.StartedAt, r.DurationMs, r.ExitCode, r.ActionsTotal, r.ActionsAllowed, r.ActionsDenied,
		r.StorageKey, r.ExpiresAt, r.IntegrityChain, r.ChainVerified, r.Truncated,
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrRunAlreadyExists
		}
		return err
	}
	return nil
}

func (p *Postgres) ListRuns(ctx context.Context, orgID string, agentName string, limit, offset int) ([]Run, error) {
	query := `SELECT id, org_id, agent_name, agent_version, policy_hash, environment,
	           started_at, duration_ms, exit_code, actions_total, actions_allowed, actions_denied,
	           storage_key, expires_at, integrity_chain, chain_verified, truncated
	          FROM runs WHERE org_id = $1`
	args := []any{orgID}
	argIdx := 2

	if agentName != "" {
		query += fmt.Sprintf(" AND agent_name = $%d", argIdx)
		args = append(args, agentName)
		argIdx++
	}
	query += " ORDER BY started_at DESC"
	query += fmt.Sprintf(" LIMIT $%d OFFSET $%d", argIdx, argIdx+1)
	args = append(args, limit, offset)

	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.OrgID, &r.AgentName, &r.AgentVersion, &r.PolicyHash,
			&r.Environment, &r.StartedAt, &r.DurationMs, &r.ExitCode,
			&r.ActionsTotal, &r.ActionsAllowed, &r.ActionsDenied, &r.StorageKey, &r.ExpiresAt,
			&r.IntegrityChain, &r.ChainVerified, &r.Truncated); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, nil
}

// ListRunsForSimulate returns runs for a single agent that started on
// or after `since`, most-recent first, capped at `limit`. Slice 13's
// diff badge calls this with limit=200 — enough to swamp the
// per-simulation budget without us either paginating or pulling
// unbounded rows.
//
// The selector is one agent name today; slice 15 will resolve labels
// to a set and call this once per resolved agent (or, more likely,
// extend the WHERE clause to take a list).
func (p *Postgres) ListRunsForSimulate(ctx context.Context, orgID, agentName string, since time.Time, limit int) ([]Run, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, org_id, agent_name, agent_version, policy_hash, environment,
		   started_at, duration_ms, exit_code, actions_total, actions_allowed, actions_denied,
		   storage_key, expires_at, integrity_chain, chain_verified, truncated
		  FROM runs
		  WHERE org_id = $1 AND agent_name = $2 AND started_at >= $3
		  ORDER BY started_at DESC
		  LIMIT $4`,
		orgID, agentName, since, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list runs for simulate: %w", err)
	}
	defer rows.Close()

	var runs []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.OrgID, &r.AgentName, &r.AgentVersion, &r.PolicyHash,
			&r.Environment, &r.StartedAt, &r.DurationMs, &r.ExitCode,
			&r.ActionsTotal, &r.ActionsAllowed, &r.ActionsDenied, &r.StorageKey, &r.ExpiresAt,
			&r.IntegrityChain, &r.ChainVerified, &r.Truncated); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, nil
}

func (p *Postgres) GetRun(ctx context.Context, orgID, runID string) (*Run, error) {
	r := &Run{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, org_id, agent_name, agent_version, policy_hash, environment,
		  started_at, duration_ms, exit_code, actions_total, actions_allowed, actions_denied,
		  storage_key, expires_at, integrity_chain, chain_verified, truncated
		 FROM runs WHERE org_id = $1 AND id = $2`, orgID, runID,
	).Scan(&r.ID, &r.OrgID, &r.AgentName, &r.AgentVersion, &r.PolicyHash,
		&r.Environment, &r.StartedAt, &r.DurationMs, &r.ExitCode,
		&r.ActionsTotal, &r.ActionsAllowed, &r.ActionsDenied, &r.StorageKey, &r.ExpiresAt,
		&r.IntegrityChain, &r.ChainVerified, &r.Truncated)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get run: %w", err)
	}
	return r, nil
}

func (p *Postgres) DeleteRun(ctx context.Context, orgID, runID string) (storageKey string, err error) {
	err = p.pool.QueryRow(ctx,
		`DELETE FROM runs WHERE org_id = $1 AND id = $2 RETURNING storage_key`, orgID, runID,
	).Scan(&storageKey)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return storageKey, err
}

// ExpiredRun is a thin tuple returned by ReapExpiredRuns — just enough
// for the retention worker to issue the S3 delete and emit an audit
// event. We deliberately don't return the full Run because the worker
// has no need for action counts, timestamps, etc.
type ExpiredRun struct {
	ID         string
	OrgID      string
	StorageKey string
}

// ReapExpiredRuns atomically deletes up to `limit` runs whose
// expires_at is older than `before` and returns their identifiers so
// the caller can also delete the backing S3 objects.
//
// The query uses `FOR UPDATE SKIP LOCKED` on the inner select so
// multiple retention workers running concurrently (HA control plane,
// rolling deploys) don't fight over the same rows. The DELETE in the
// outer statement only sees the rows the inner select locked, which
// is what we want — anything another worker grabbed simultaneously
// silently skips here.
//
// The DELETE happens before the S3 cleanup in the caller; if the S3
// delete fails we leave an orphan object (logged + flagged for a
// follow-up sweep) but the user can no longer reach it via the API.
// We prefer that ordering over leaving DB rows pointing at deleted
// objects, which would 500 the download endpoint.
func (p *Postgres) ReapExpiredRuns(ctx context.Context, before time.Time, limit int) ([]ExpiredRun, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.pool.Query(ctx, `
		DELETE FROM runs
		WHERE id IN (
		  SELECT id FROM runs
		  WHERE expires_at < $1
		  ORDER BY expires_at
		  LIMIT $2
		  FOR UPDATE SKIP LOCKED
		)
		RETURNING id, org_id, storage_key
	`, before, limit)
	if err != nil {
		return nil, fmt.Errorf("reap expired runs: %w", err)
	}
	defer rows.Close()
	out := make([]ExpiredRun, 0, limit)
	for rows.Next() {
		var e ExpiredRun
		if err := rows.Scan(&e.ID, &e.OrgID, &e.StorageKey); err != nil {
			return out, fmt.Errorf("scan expired run: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountExpiredRuns reports how many run rows are past their TTL right
// now. Exposed for /readyz, /metrics, and operational dashboards;
// running it on a hot path is cheap because expires_at is indexed.
func (p *Postgres) CountExpiredRuns(ctx context.Context, before time.Time) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx,
		`SELECT count(*) FROM runs WHERE expires_at < $1`, before,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count expired runs: %w", err)
	}
	return n, nil
}

// --- Agents ---

type Agent struct {
	ID               string     `json:"id"`
	OrgID            string     `json:"org_id"`
	Name             string     `json:"name"`
	Version          string     `json:"version"`
	IdentityManifest []byte     `json:"identity_manifest"`
	CapabilitiesHash string     `json:"capabilities_hash"`
	PolicyHash       string     `json:"policy_hash"`
	RegisteredAt     time.Time  `json:"registered_at"`
	LastSeen         time.Time  `json:"last_seen"`

	// Slice 15: AppliedPolicyHash records the hash the agent most
	// recently confirmed via POST /v1/agents/:name/policy_applied.
	// Both fields are nil until the agent reports for the first time.
	AppliedPolicyHash *string    `json:"applied_policy_hash,omitempty"`
	AppliedAt         *time.Time `json:"applied_at,omitempty"`
}

type AgentBaseline struct {
	AgentID          string    `json:"agent_id"`
	ComputedAt       time.Time `json:"computed_at"`
	WindowDays       int       `json:"window_days"`
	AvgActionsPerRun float64   `json:"avg_actions_per_run"`
	AvgDenialsPerRun float64   `json:"avg_denials_per_run"`
	ToolDistribution []byte    `json:"tool_distribution"`
}

type AuditEvent struct {
	ID         string    `json:"id"`
	OrgID      string    `json:"org_id"`
	UserID     string    `json:"user_id"`
	Action     string    `json:"action"`
	Resource   string    `json:"resource"`
	ResourceID string    `json:"resource_id"`
	Metadata   []byte    `json:"metadata"`
	CreatedAt  time.Time `json:"created_at"`
}

// UpdateAgentLastSeen bumps the `last_seen` column for a (org, name)
// pair. Called on every successful trace upload and on every streaming
// intent so the dashboard's "Online" pill reflects what's actually
// happening, not just when an agent re-registered.
//
// Silent no-op when no row matches: handleUploadTrace doesn't require
// the agent to be pre-registered, so a missing row is normal during
// the first run of an agent that hasn't called POST /v1/agents yet.
func (p *Postgres) UpdateAgentLastSeen(ctx context.Context, orgID, agentName string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE agents SET last_seen = now() WHERE org_id = $1 AND name = $2`,
		orgID, agentName,
	)
	return err
}

func (p *Postgres) UpsertAgent(ctx context.Context, a *Agent) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO agents (org_id, name, version, identity_manifest, capabilities_hash, policy_hash)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (org_id, name) DO UPDATE SET
		   version = EXCLUDED.version,
		   identity_manifest = EXCLUDED.identity_manifest,
		   capabilities_hash = EXCLUDED.capabilities_hash,
		   policy_hash = EXCLUDED.policy_hash,
		   last_seen = now()`,
		a.OrgID, a.Name, a.Version, a.IdentityManifest, a.CapabilitiesHash, a.PolicyHash,
	)
	return err
}

func (p *Postgres) ListAgents(ctx context.Context, orgID string) ([]Agent, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, org_id, name, version, identity_manifest, capabilities_hash, policy_hash,
		        registered_at, last_seen, applied_policy_hash, applied_at
		 FROM agents WHERE org_id = $1 ORDER BY name`, orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var agents []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Name, &a.Version, &a.IdentityManifest,
			&a.CapabilitiesHash, &a.PolicyHash, &a.RegisteredAt, &a.LastSeen,
			&a.AppliedPolicyHash, &a.AppliedAt); err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, nil
}

func (p *Postgres) GetAgent(ctx context.Context, orgID, name string) (*Agent, error) {
	a := &Agent{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, org_id, name, version, identity_manifest, capabilities_hash, policy_hash,
		        registered_at, last_seen, applied_policy_hash, applied_at
		 FROM agents WHERE org_id = $1 AND name = $2`, orgID, name,
	).Scan(&a.ID, &a.OrgID, &a.Name, &a.Version, &a.IdentityManifest,
		&a.CapabilitiesHash, &a.PolicyHash, &a.RegisteredAt, &a.LastSeen,
		&a.AppliedPolicyHash, &a.AppliedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (p *Postgres) GetAgentPolicy(ctx context.Context, orgID, agentName string) (string, error) {
	var policyYAML string
	err := p.pool.QueryRow(ctx,
		`SELECT COALESCE(policy_yaml, '') FROM agents WHERE org_id = $1 AND name = $2`, orgID, agentName,
	).Scan(&policyYAML)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get agent policy: %w", err)
	}
	return policyYAML, nil
}

func (p *Postgres) UpdateAgentPolicy(ctx context.Context, orgID, agentName, policyYAML string) error {
	_, err := p.pool.Exec(ctx,
		`UPDATE agents SET policy_yaml = $1, policy_updated_at = now() WHERE org_id = $2 AND name = $3`,
		policyYAML, orgID, agentName,
	)
	if err != nil {
		return fmt.Errorf("update agent policy: %w", err)
	}
	return nil
}

// GetAgentBaseline returns the most recent baseline for agentID, but
// only if the agent belongs to orgID. The join into the agents table
// is required even though the API handler already verified org
// ownership — defense-in-depth catches future callers that forget.
func (p *Postgres) GetAgentBaseline(ctx context.Context, orgID, agentID string) (*AgentBaseline, error) {
	b := &AgentBaseline{}
	err := p.pool.QueryRow(ctx,
		`SELECT b.agent_id, b.computed_at, b.window_days, b.avg_actions_per_run, b.avg_denials_per_run, b.tool_distribution
		 FROM agent_baselines b
		 INNER JOIN agents a ON a.id = b.agent_id
		 WHERE b.agent_id = $1 AND a.org_id = $2
		 ORDER BY b.computed_at DESC LIMIT 1`, agentID, orgID,
	).Scan(&b.AgentID, &b.ComputedAt, &b.WindowDays, &b.AvgActionsPerRun, &b.AvgDenialsPerRun, &b.ToolDistribution)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent baseline: %w", err)
	}
	return b, nil
}

func (p *Postgres) InsertBaseline(ctx context.Context, baseline *AgentBaseline) error {
	_, err := p.pool.Exec(ctx,
		`INSERT INTO agent_baselines (agent_id, computed_at, window_days, avg_actions_per_run, avg_denials_per_run, tool_distribution)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		baseline.AgentID, baseline.ComputedAt, baseline.WindowDays,
		baseline.AvgActionsPerRun, baseline.AvgDenialsPerRun, baseline.ToolDistribution,
	)
	if err != nil {
		return fmt.Errorf("insert baseline: %w", err)
	}
	return nil
}

// ComputeBaseline rolls up the last windowDays of runs for agentID
// into an AgentBaseline row. The orgID parameter scopes the agent
// lookup; passing the wrong org returns "agent not found" rather than
// leaking the existence of an agent in another tenant.
func (p *Postgres) ComputeBaseline(ctx context.Context, orgID, agentID string, windowDays int) (*AgentBaseline, error) {
	agent, err := p.GetAgentByID(ctx, orgID, agentID)
	if err != nil {
		return nil, fmt.Errorf("get agent: %w", err)
	}
	if agent == nil {
		return nil, fmt.Errorf("agent not found")
	}

	var avgActions, avgDenials float64
	var runCount int
	err = p.pool.QueryRow(ctx,
		`SELECT COALESCE(AVG(actions_total::float), 0), COALESCE(AVG(actions_denied::float), 0), COUNT(*)
		 FROM runs
		 WHERE org_id = $1 AND agent_name = $2
		   AND started_at >= now() - ($3 * interval '1 day')`,
		agent.OrgID, agent.Name, windowDays,
	).Scan(&avgActions, &avgDenials, &runCount)
	if err != nil {
		return nil, fmt.Errorf("compute baseline: %w", err)
	}

	emptyToolDist, _ := json.Marshal(map[string]any{})
	baseline := &AgentBaseline{
		AgentID:          agentID,
		ComputedAt:       time.Now(),
		WindowDays:       windowDays,
		AvgActionsPerRun: avgActions,
		AvgDenialsPerRun: avgDenials,
		ToolDistribution: emptyToolDist,
	}
	return baseline, nil
}

// GetAgentByID returns the agent with the given id, scoped to orgID.
// Returns nil if the agent doesn't exist or belongs to a different org —
// callers cannot distinguish the two and that's intentional, mirroring
// the not-found vs not-allowed conflation we want for tenant isolation.
func (p *Postgres) GetAgentByID(ctx context.Context, orgID, agentID string) (*Agent, error) {
	a := &Agent{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, org_id, name, version, identity_manifest, capabilities_hash, policy_hash,
		        registered_at, last_seen, applied_policy_hash, applied_at
		 FROM agents WHERE id = $1 AND org_id = $2`, agentID, orgID,
	).Scan(&a.ID, &a.OrgID, &a.Name, &a.Version, &a.IdentityManifest,
		&a.CapabilitiesHash, &a.PolicyHash, &a.RegisteredAt, &a.LastSeen,
		&a.AppliedPolicyHash, &a.AppliedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get agent by id: %w", err)
	}
	return a, nil
}

func (p *Postgres) InsertAuditEvent(ctx context.Context, orgID, userID, action, resource, resourceID string, metadata []byte) error {
	if metadata == nil {
		metadata = []byte("{}")
	}
	_, err := p.pool.Exec(ctx,
		`INSERT INTO audit_events (org_id, user_id, action, resource, resource_id, metadata)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		orgID, userID, action, resource, resourceID, metadata,
	)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

func (p *Postgres) ListAuditEvents(ctx context.Context, orgID string, limit, offset int) ([]AuditEvent, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, org_id, user_id, action, resource, resource_id, metadata, created_at
		 FROM audit_events WHERE org_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`,
		orgID, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()

	var events []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.OrgID, &e.UserID, &e.Action, &e.Resource, &e.ResourceID, &e.Metadata, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		events = append(events, e)
	}
	return events, nil
}

// ListAuditEventsInWindow returns audit events for an org within
// [start, end). Used by the evidence-pack export to scope an audit
// log to a compliance period. Ordered by created_at ASC so the pack
// reads chronologically.
func (p *Postgres) ListAuditEventsInWindow(ctx context.Context, orgID string, start, end time.Time) ([]AuditEvent, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, org_id, user_id, action, resource, resource_id, metadata, created_at
		 FROM audit_events
		 WHERE org_id = $1 AND created_at >= $2 AND created_at < $3
		 ORDER BY created_at ASC`,
		orgID, start, end,
	)
	if err != nil {
		return nil, fmt.Errorf("list audit events window: %w", err)
	}
	defer rows.Close()
	var events []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.OrgID, &e.UserID, &e.Action, &e.Resource, &e.ResourceID, &e.Metadata, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan audit event: %w", err)
		}
		events = append(events, e)
	}
	return events, nil
}

// ListRunsInWindow returns runs for an org within [start, end).
// Used by the evidence-pack export to capture which agent runs
// happened during the compliance period.
func (p *Postgres) ListRunsInWindow(ctx context.Context, orgID string, start, end time.Time) ([]Run, error) {
	rows, err := p.pool.Query(ctx,
		`SELECT id, org_id, agent_name, agent_version, policy_hash, environment,
		        started_at, duration_ms, exit_code,
		        actions_total, actions_allowed, actions_denied,
		        storage_key, expires_at, integrity_chain, chain_verified, truncated
		 FROM runs
		 WHERE org_id = $1 AND started_at >= $2 AND started_at < $3
		 ORDER BY started_at ASC`,
		orgID, start, end,
	)
	if err != nil {
		return nil, fmt.Errorf("list runs window: %w", err)
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		if err := rows.Scan(&r.ID, &r.OrgID, &r.AgentName, &r.AgentVersion, &r.PolicyHash, &r.Environment,
			&r.StartedAt, &r.DurationMs, &r.ExitCode,
			&r.ActionsTotal, &r.ActionsAllowed, &r.ActionsDenied,
			&r.StorageKey, &r.ExpiresAt, &r.IntegrityChain, &r.ChainVerified, &r.Truncated); err != nil {
			return nil, fmt.Errorf("scan run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Postgres) ListOrgs(ctx context.Context) ([]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT id FROM organizations ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list orgs: %w", err)
	}
	defer rows.Close()

	var orgIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan org id: %w", err)
		}
		orgIDs = append(orgIDs, id)
	}
	return orgIDs, nil
}

// GetUserByID returns the user with the given id, scoped to orgID.
// A non-empty orgID is required — passing "" returns nil to prevent
// cross-tenant smuggling via empty-string JWT claims.
func (p *Postgres) GetUserByID(ctx context.Context, orgID, userID string) (*User, error) {
	if orgID == "" || userID == "" {
		return nil, nil
	}
	u := &User{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, org_id, email, role, created_at FROM users WHERE id = $1 AND org_id = $2`, userID, orgID,
	).Scan(&u.ID, &u.OrgID, &u.Email, &u.Role, &u.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	return u, nil
}

// --- Proposals ---

type Proposal struct {
	ID           string     `json:"id"`
	OrgID        string     `json:"org_id"`
	AgentName    string     `json:"agent_name"`
	Status       string     `json:"status"`
	TriggerType  string     `json:"trigger_type"`
	Evidence     []byte     `json:"evidence"`
	ProposedRule []byte     `json:"proposed_rule"`
	CreatedAt    time.Time  `json:"created_at"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	DecidedBy    *string    `json:"decided_by,omitempty"`
}

func (p *Postgres) InsertProposal(ctx context.Context, pr *Proposal) error {
	pr.ID = ulid.Make().String()
	_, err := p.pool.Exec(ctx,
		`INSERT INTO proposals (id, org_id, agent_name, status, trigger_type, evidence, proposed_rule)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		pr.ID, pr.OrgID, pr.AgentName, "pending", pr.TriggerType, pr.Evidence, pr.ProposedRule,
	)
	return err
}

func (p *Postgres) ListProposals(ctx context.Context, orgID, status string) ([]Proposal, error) {
	query := `SELECT id, org_id, agent_name, status, trigger_type, evidence, proposed_rule, created_at, decided_at, decided_by
	          FROM proposals WHERE org_id = $1`
	args := []any{orgID}
	if status != "" {
		query += " AND status = $2"
		args = append(args, status)
	}
	query += " ORDER BY created_at DESC"

	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var proposals []Proposal
	for rows.Next() {
		var pr Proposal
		if err := rows.Scan(&pr.ID, &pr.OrgID, &pr.AgentName, &pr.Status, &pr.TriggerType,
			&pr.Evidence, &pr.ProposedRule, &pr.CreatedAt, &pr.DecidedAt, &pr.DecidedBy); err != nil {
			return nil, err
		}
		proposals = append(proposals, pr)
	}
	return proposals, nil
}

// UpdateProposalStatus sets the status, decided_at, and decided_by columns
// on a proposal scoped to orgID. The boolean return distinguishes between
// "no rows matched" (proposal id is wrong or belongs to another org) and
// "row updated"; callers should turn no-match into 404.
//
// decided_by may be empty for system-initiated transitions (e.g.
// expiration sweeps), so we pass a nullable string to the column.
func (p *Postgres) UpdateProposalStatus(ctx context.Context, orgID, proposalID, status, userID string) (bool, error) {
	var decidedBy any
	if userID != "" {
		decidedBy = userID
	}
	tag, err := p.pool.Exec(ctx,
		`UPDATE proposals SET status = $1, decided_at = now(), decided_by = $2
		 WHERE id = $3 AND org_id = $4`,
		status, decidedBy, proposalID, orgID,
	)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// --- Users ---

type User struct {
	ID           string    `json:"id"`
	OrgID        string    `json:"org_id"`
	Email        string    `json:"email"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	PasswordHash string    `json:"-"` // never serialized
	AuthProvider string    `json:"auth_provider"`
}

func (p *Postgres) CreateUser(ctx context.Context, orgID, email, role string) (*User, error) {
	u := &User{}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO users (org_id, email, role) VALUES ($1, $2, $3)
		 RETURNING id, org_id, email, role, created_at, COALESCE(password_hash, ''), auth_provider`,
		orgID, email, role,
	).Scan(&u.ID, &u.OrgID, &u.Email, &u.Role, &u.CreatedAt, &u.PasswordHash, &u.AuthProvider)
	return u, err
}

// CreateUserWithPassword inserts a user with a bcrypt password hash and
// an explicit auth_provider tag. Used by the LocalAuth adapter to
// bootstrap the first admin and to provision invited team members.
// Pass authProvider as "local" / "supabase" / "oidc:<issuer>" so the
// API can later refuse cross-provider login attempts.
func (p *Postgres) CreateUserWithPassword(ctx context.Context, orgID, email, role, passwordHash, authProvider string) (*User, error) {
	u := &User{}
	err := p.pool.QueryRow(ctx,
		`INSERT INTO users (org_id, email, role, password_hash, auth_provider)
		 VALUES ($1,$2,$3,$4,$5)
		 RETURNING id, org_id, email, role, created_at, COALESCE(password_hash, ''), auth_provider`,
		orgID, email, role, passwordHash, authProvider,
	).Scan(&u.ID, &u.OrgID, &u.Email, &u.Role, &u.CreatedAt, &u.PasswordHash, &u.AuthProvider)
	return u, err
}

// GetUserByEmail returns the user with the given email inside orgID.
// Lookups by email are inherently cross-tenant if unscoped (a customer
// at one org could enumerate users at another), so orgID is required.
func (p *Postgres) GetUserByEmail(ctx context.Context, orgID, email string) (*User, error) {
	if orgID == "" || email == "" {
		return nil, nil
	}
	u := &User{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, org_id, email, role, created_at, COALESCE(password_hash, ''), auth_provider
		 FROM users WHERE email = $1 AND org_id = $2`, email, orgID,
	).Scan(&u.ID, &u.OrgID, &u.Email, &u.Role, &u.CreatedAt, &u.PasswordHash, &u.AuthProvider)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// LookupUserForLogin returns the user matching email + auth_provider
// across all orgs. The users table has a UNIQUE constraint on email,
// so this returns at most one row. Required for local-auth and OIDC
// login, where the caller knows email but not org_id at sign-in time.
// Refuses to match if the user was created by a different provider.
func (p *Postgres) LookupUserForLogin(ctx context.Context, email, authProvider string) (*User, error) {
	if email == "" {
		return nil, nil
	}
	u := &User{}
	err := p.pool.QueryRow(ctx,
		`SELECT id, org_id, email, role, created_at, COALESCE(password_hash, ''), auth_provider
		 FROM users WHERE email = $1 AND auth_provider = $2`, email, authProvider,
	).Scan(&u.ID, &u.OrgID, &u.Email, &u.Role, &u.CreatedAt, &u.PasswordHash, &u.AuthProvider)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return u, err
}

// CountUsers returns the total number of users across all orgs.
func (p *Postgres) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n)
	return n, err
}

// CountUsersByProvider returns the number of users created by a
// specific auth_provider tag. The local-auth bootstrap uses this to
// know whether it has provisioned an admin yet, independent of any
// Supabase / OIDC users that may have been created earlier.
func (p *Postgres) CountUsersByProvider(ctx context.Context, provider string) (int, error) {
	var n int
	err := p.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE auth_provider = $1`, provider).Scan(&n)
	return n, err
}

// --- Policy sets & agent labels (Slice 15) ---

// PolicySet is the editing unit for universal policy enforcement. A
// single set carries a YAML body and a selector that resolves at
// publish time to an agent set. Updates bump version + recompute
// policy_hash server-side.
type PolicySet struct {
	ID         string          `json:"id"`
	OrgID      string          `json:"org_id"`
	Name       string          `json:"name"`
	Selector   json.RawMessage `json:"selector"`
	PolicyYAML string          `json:"policy_yaml"`
	PolicyHash string          `json:"policy_hash"`
	Version    int             `json:"version"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

// UpsertPolicySet creates or updates by (org_id, name). The caller is
// responsible for computing PolicyHash; we don't recompute here so the
// hash stays consistent with whatever the runtime + simulator pin off.
// Returns the resulting row with version reflecting the upsert.
func (p *Postgres) UpsertPolicySet(ctx context.Context, s *PolicySet) error {
	return p.pool.QueryRow(ctx, `
		INSERT INTO policy_sets (org_id, name, selector, policy_yaml, policy_hash, version)
		VALUES ($1, $2, $3, $4, $5, 1)
		ON CONFLICT (org_id, name) DO UPDATE SET
		  selector    = EXCLUDED.selector,
		  policy_yaml = EXCLUDED.policy_yaml,
		  policy_hash = EXCLUDED.policy_hash,
		  version     = policy_sets.version + 1,
		  updated_at  = now()
		RETURNING id, version, created_at, updated_at
	`, s.OrgID, s.Name, s.Selector, s.PolicyYAML, s.PolicyHash,
	).Scan(&s.ID, &s.Version, &s.CreatedAt, &s.UpdatedAt)
}

// GetPolicySetByID returns the named set scoped to org. Nil-on-miss
// mirrors GetAgent — callers can't distinguish "not found" from "not
// allowed", which is the tenant-isolation behavior we want.
func (p *Postgres) GetPolicySetByID(ctx context.Context, orgID, id string) (*PolicySet, error) {
	s := &PolicySet{}
	err := p.pool.QueryRow(ctx, `
		SELECT id, org_id, name, selector, policy_yaml, policy_hash, version, created_at, updated_at
		FROM policy_sets WHERE org_id = $1 AND id = $2
	`, orgID, id).Scan(&s.ID, &s.OrgID, &s.Name, &s.Selector, &s.PolicyYAML, &s.PolicyHash, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get policy set: %w", err)
	}
	return s, nil
}

// GetPolicySetByName is the slice-15 read path the app's editor uses
// when the operator addresses a set by name (the demo path).
func (p *Postgres) GetPolicySetByName(ctx context.Context, orgID, name string) (*PolicySet, error) {
	s := &PolicySet{}
	err := p.pool.QueryRow(ctx, `
		SELECT id, org_id, name, selector, policy_yaml, policy_hash, version, created_at, updated_at
		FROM policy_sets WHERE org_id = $1 AND name = $2
	`, orgID, name).Scan(&s.ID, &s.OrgID, &s.Name, &s.Selector, &s.PolicyYAML, &s.PolicyHash, &s.Version, &s.CreatedAt, &s.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get policy set by name: %w", err)
	}
	return s, nil
}

// SetAgentLabels overwrites the label set for an agent: deletes
// existing rows, inserts the new ones, all in one transaction.
// "Overwrites" rather than "merges" because that's the mental model
// operators have ("env=prod tier=primary" replaces whatever was there).
func (p *Postgres) SetAgentLabels(ctx context.Context, orgID, agentName string, labels map[string]string) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set agent labels: begin: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var agentID string
	err = tx.QueryRow(ctx,
		`SELECT id FROM agents WHERE org_id = $1 AND name = $2`, orgID, agentName,
	).Scan(&agentID)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("set agent labels: agent %q not found", agentName)
	}
	if err != nil {
		return fmt.Errorf("set agent labels: resolve agent: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM agent_labels WHERE agent_id = $1`, agentID); err != nil {
		return fmt.Errorf("set agent labels: delete: %w", err)
	}

	for k, v := range labels {
		if _, err := tx.Exec(ctx,
			`INSERT INTO agent_labels (agent_id, key, value) VALUES ($1, $2, $3)`,
			agentID, k, v,
		); err != nil {
			return fmt.Errorf("set agent labels: insert %q: %w", k, err)
		}
	}
	return tx.Commit(ctx)
}

// GetAgentLabels returns the agent's current label set. Empty map when
// the agent has no labels (still a valid state — the agent may match
// only by name).
func (p *Postgres) GetAgentLabels(ctx context.Context, orgID, agentName string) (map[string]string, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT al.key, al.value FROM agent_labels al
		JOIN agents a ON a.id = al.agent_id
		WHERE a.org_id = $1 AND a.name = $2
	`, orgID, agentName)
	if err != nil {
		return nil, fmt.Errorf("get agent labels: %w", err)
	}
	defer rows.Close()
	labels := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		labels[k] = v
	}
	return labels, nil
}

// ResolveSelector returns the agents matching a slice-15 selector.
// Supports two arms:
//
//	{ "agent_name": "code-assist" }
//	{ "match": { "env": "prod", "tier": "primary" } }   // AND across keys
//
// The match arm is implemented as a single SQL query that joins
// agent_labels back to itself once per key — fine for the slice-15
// scale (a few keys per selector). Future grammar additions (any_of,
// not_match) would warrant a query builder; we don't pre-build that
// today.
func (p *Postgres) ResolveSelector(ctx context.Context, orgID string, selector json.RawMessage) ([]Agent, error) {
	var parsed struct {
		AgentName string            `json:"agent_name"`
		Match     map[string]string `json:"match"`
	}
	if err := json.Unmarshal(selector, &parsed); err != nil {
		return nil, fmt.Errorf("resolve selector: invalid JSON: %w", err)
	}

	if parsed.AgentName != "" {
		a, err := p.GetAgent(ctx, orgID, parsed.AgentName)
		if err != nil || a == nil {
			return nil, err
		}
		return []Agent{*a}, nil
	}

	if len(parsed.Match) == 0 {
		return nil, fmt.Errorf("resolve selector: empty selector")
	}

	// Build a query that requires every (key, value) pair to match.
	// `GROUP BY a.id HAVING COUNT(DISTINCT al.key) = $N` is the
	// idiomatic SQL form for label-AND match, where N is the number
	// of required keys.
	args := []any{orgID}
	conds := []string{}
	for k, v := range parsed.Match {
		args = append(args, k, v)
		conds = append(conds, fmt.Sprintf("(al.key = $%d AND al.value = $%d)", len(args)-1, len(args)))
	}
	args = append(args, len(parsed.Match))

	query := fmt.Sprintf(`
		SELECT a.id, a.org_id, a.name, a.version, a.identity_manifest, a.capabilities_hash, a.policy_hash,
		       a.registered_at, a.last_seen, a.applied_policy_hash, a.applied_at
		FROM agents a
		JOIN agent_labels al ON al.agent_id = a.id
		WHERE a.org_id = $1 AND (%s)
		GROUP BY a.id
		HAVING COUNT(DISTINCT al.key) = $%d
		ORDER BY a.name
	`, strings.Join(conds, " OR "), len(args))

	rows, err := p.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve selector: query: %w", err)
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Name, &a.Version, &a.IdentityManifest,
			&a.CapabilitiesHash, &a.PolicyHash, &a.RegisteredAt, &a.LastSeen,
			&a.AppliedPolicyHash, &a.AppliedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// MarkPolicyApplied advances the agent's applied-state. Called from
// POST /v1/agents/:name/policy_applied. We update by name (not id)
// because the runtime knows its name, not the platform-side UUID.
func (p *Postgres) MarkPolicyApplied(ctx context.Context, orgID, agentName, hash string) error {
	res, err := p.pool.Exec(ctx,
		`UPDATE agents SET applied_policy_hash = $1, applied_at = now()
		 WHERE org_id = $2 AND name = $3`,
		hash, orgID, agentName,
	)
	if err != nil {
		return fmt.Errorf("mark policy applied: %w", err)
	}
	if res.RowsAffected() == 0 {
		return fmt.Errorf("mark policy applied: agent %q not found", agentName)
	}
	return nil
}
