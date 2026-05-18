// Package migrate ports the docker-compose bash migration runner into
// Go so it can be:
//
//   1. Invoked outside Docker via `relic-api migrate up`.
//   2. Tested with a real Postgres connection.
//   3. Used by the backup/restore command to know what to expect.
//
// The behavior matches the bash runner exactly:
//   - migrations/                always applied (tracked by bare filename)
//   - migrations.supabase/       only when RELIC_AUTH_MODE=supabase
//                                (tracked as "supabase/<filename>")
//   - migrations.rls/            only when RELIC_RLS_ENABLED=true
//                                (tracked as "rls/<filename>")
//
// Re-runs are idempotent: every applied file is recorded in
// schema_migrations(filename) and skipped on subsequent runs.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Options controls which migration folders get applied.
type Options struct {
	// CoreDir is the always-applied migration folder. Required.
	CoreDir string
	// SupabaseDir holds Supabase-only migrations. Applied only when
	// AuthMode == "supabase". Empty disables.
	SupabaseDir string
	// RLSDir holds RLS-only migrations. Applied only when
	// RLSEnabled is true. Empty disables.
	RLSDir string
	// AuthMode is the RELIC_AUTH_MODE value; the folder gate.
	AuthMode string
	// RLSEnabled mirrors RELIC_RLS_ENABLED.
	RLSEnabled bool
}

// Runner applies migrations against a Postgres pool.
type Runner struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	opts   Options
}

func New(pool *pgxpool.Pool, opts Options, logger *slog.Logger) *Runner {
	return &Runner{pool: pool, opts: opts, logger: logger}
}

// Up applies every pending migration. Returns the count of files
// applied this run, and an error if any single file failed.
func (r *Runner) Up(ctx context.Context) (int, error) {
	if err := r.ensureTrackingTable(ctx); err != nil {
		return 0, fmt.Errorf("ensure schema_migrations: %w", err)
	}

	// Supabase mode needs an auth.users stub before migration 006's
	// successor (and the supabase-only trigger) reference it. Real
	// Supabase databases provision auth.users automatically; in
	// self-host-supabase mode we create an empty shim so the trigger
	// has something to attach to.
	if r.opts.AuthMode == "supabase" {
		if err := r.createAuthUsersStub(ctx); err != nil {
			return 0, fmt.Errorf("create auth.users stub: %w", err)
		}
	}

	applied := 0

	n, err := r.applyFolder(ctx, r.opts.CoreDir, "")
	if err != nil {
		return applied, fmt.Errorf("apply core migrations: %w", err)
	}
	applied += n

	if r.opts.AuthMode == "supabase" && r.opts.SupabaseDir != "" {
		n, err := r.applyFolder(ctx, r.opts.SupabaseDir, "supabase")
		if err != nil {
			return applied, fmt.Errorf("apply supabase migrations: %w", err)
		}
		applied += n
	}

	if r.opts.RLSEnabled && r.opts.RLSDir != "" {
		n, err := r.applyFolder(ctx, r.opts.RLSDir, "rls")
		if err != nil {
			return applied, fmt.Errorf("apply rls migrations: %w", err)
		}
		applied += n
	}

	return applied, nil
}

// Status returns the list of applied migration filenames in lexical
// order (matches schema_migrations sort).
func (r *Runner) Status(ctx context.Context) ([]string, error) {
	if err := r.ensureTrackingTable(ctx); err != nil {
		return nil, err
	}
	rows, err := r.pool.Query(ctx, `SELECT filename FROM schema_migrations ORDER BY filename`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var fn string
		if err := rows.Scan(&fn); err != nil {
			return nil, err
		}
		out = append(out, fn)
	}
	return out, rows.Err()
}

// ----- internals -----

func (r *Runner) ensureTrackingTable(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			filename   TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`)
	return err
}

func (r *Runner) createAuthUsersStub(ctx context.Context) error {
	_, err := r.pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS auth`)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS auth.users (
			id                UUID PRIMARY KEY,
			email             TEXT,
			raw_app_meta_data JSONB
		)
	`)
	return err
}

func (r *Runner) applyFolder(ctx context.Context, dir, prefix string) (int, error) {
	if dir == "" {
		return 0, nil
	}
	files, err := listSQL(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("list %s: %w", dir, err)
	}
	applied := 0
	for _, f := range files {
		// Track by "<prefix>/<filename>" for folder migrations so the
		// optional sets don't collide with the core sequence.
		key := filepath.Base(f)
		if prefix != "" {
			key = prefix + "/" + key
		}
		var present bool
		if err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE filename = $1)`, key).Scan(&present); err != nil {
			return applied, fmt.Errorf("check %s: %w", key, err)
		}
		if present {
			r.logger.Info("migration already applied", "file", key)
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			return applied, fmt.Errorf("read %s: %w", f, err)
		}
		r.logger.Info("applying migration", "file", key)
		if _, err := r.pool.Exec(ctx, string(body)); err != nil {
			return applied, fmt.Errorf("execute %s: %w", key, err)
		}
		if _, err := r.pool.Exec(ctx, `INSERT INTO schema_migrations (filename) VALUES ($1)`, key); err != nil {
			return applied, fmt.Errorf("record %s: %w", key, err)
		}
		applied++
	}
	return applied, nil
}

func listSQL(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}
