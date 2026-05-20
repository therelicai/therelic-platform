package storage

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Replica holds a separate connection pool that targets a read-only
// replica. The primary Postgres pool still handles writes and any
// reads that need strong consistency; replica-routed reads accept
// the small lag for the much larger headroom.
//
// Resolution rules:
//   - DATABASE_REPLICA_URL unset:  Replica() returns the primary pool
//     so handlers never need to nil-check.
//   - DATABASE_REPLICA_URL set:    Replica() returns the replica pool.
//
// When you add a new read handler, decide whether replica-lag matters
// for its semantics. If yes (e.g. read-after-write inside a request
// that just inserted), use Primary. If no (audit log listing, metrics
// aggregations), use Replica.

// WithReplica attaches a read-replica pool to the Postgres helper.
// Returns the same *Postgres for fluent chaining at boot. Pass a nil
// pool to leave the field unset; Readonly() then falls back to the
// primary.
func (p *Postgres) WithReplica(pool *pgxpool.Pool) *Postgres {
	p.replica = pool
	return p
}

// Readonly returns the pool reads should target. The pool returned is
// either the configured replica or the primary as a fallback.
func (p *Postgres) Readonly() *pgxpool.Pool {
	if p.replica != nil {
		return p.replica
	}
	return p.pool
}

// NewReplicaPool builds a pgx pool against the given replica URL.
// Same connection-config knobs as the primary; replicas typically
// need a larger MaxConns (read-heavy traffic) and a shorter
// MaxConnIdleTime (replicas often have aggressive idle timeouts).
func NewReplicaPool(ctx context.Context, url string, cfg PoolConfig, logger *slog.Logger) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse replica DATABASE_URL: %w", err)
	}
	if cfg.MaxConns > 0 {
		pcfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		pcfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		pcfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		pcfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("connect replica: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping replica: %w", err)
	}
	logger.Info("replica pool ready", "max_conns", pcfg.MaxConns)
	return pool, nil
}
