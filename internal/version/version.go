// Package version exposes platform build + schema version metadata.
// The values flow into:
//   - GET /v1/version  (the wire surface)
//   - relic-api logs   (so deploys are identifiable in dashboards)
//   - the opt-in telemetry ping
//
// Build flags set via -ldflags at compile time:
//   -X github.com/therelicai/therelic-platform/internal/version.Build=v0.2.0
//   -X github.com/therelicai/therelic-platform/internal/version.Commit=abc1234
// Default values are useful for go test / go run paths where no flags
// land.
package version

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Build is set at compile time via -ldflags. The CHANGELOG version is
// the source of truth; this is just where the binary self-reports.
var Build = "dev"

// Commit is the git SHA, set at compile time via -ldflags. Optional;
// "unknown" is fine in development.
var Commit = "unknown"

// SchemaVersion returns the highest-numbered migration filename that
// has been applied. The migrations runner records every applied file
// in schema_migrations(filename); we return the lexically-greatest
// filename so consumers can compare across deploys.
//
// Returns ("", nil) when the table doesn't exist yet (pre-bootstrap).
func SchemaVersion(ctx context.Context, pool *pgxpool.Pool) (string, error) {
	var fn string
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(MAX(filename), '')
		FROM schema_migrations
		WHERE filename NOT LIKE 'rls/%'
		  AND filename NOT LIKE 'supabase/%'
	`).Scan(&fn)
	if err != nil {
		// Table-missing is the pre-bootstrap state; report empty
		// rather than propagating, so a fresh /readyz can still
		// answer the platform-is-up question.
		return "", nil
	}
	return fn, nil
}
