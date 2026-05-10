// Package retention provides a background sweeper that deletes
// expired trace runs from Postgres + S3.
//
// The "30-day TTL" promised in the upload handler used to be a lie:
// expires_at was set on each row but nothing ever consulted it.
// Operators would discover S3 buckets growing unboundedly months
// after deploy. This package closes that loop.
//
// Design notes:
//
//   - Single goroutine per process. The DB query uses FOR UPDATE
//     SKIP LOCKED so running multiple control-plane replicas is
//     still safe (rolling deploys, HA setups).
//   - DB delete happens before S3 delete. An orphaned S3 object is
//     a billing problem (the next sweep will re-attempt via a
//     separate "orphan reconciliation" job — Slice 8/12 territory);
//     an orphan DB row pointing at a missing S3 object 500s the
//     download endpoint, which is user-visible. We prefer the
//     former.
//   - Errors are logged and counted but do NOT halt the worker.
//     Retention has to be best-effort because S3 outages, network
//     blips, and credential rotations all happen in the real world
//     and we'd rather sweep what we can than stop sweeping entirely.
package retention

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/therelicai/therelic-platform/internal/storage"
)

// DefaultInterval is how often the worker scans for expired rows
// when no override is provided. One hour is the right balance: short
// enough that 30-day TTLs land within ~hour-of-expiry, long enough
// that the worker doesn't generate noticeable DB load.
const DefaultInterval = time.Hour

// DefaultBatchSize is how many rows the worker reaps per scan. Caps
// the unit of work so a backlog (e.g., the worker was down for a
// month and 10M rows expired) doesn't run a single DELETE that locks
// the table for minutes.
const DefaultBatchSize = 200

// S3 is the subset of storage.S3 that the worker needs. Defined as
// an interface so retention_test.go can inject a fake without
// standing up MinIO.
type S3 interface {
	Delete(ctx context.Context, key string) error
}

// DB is the subset of storage.Postgres the worker needs.
type DB interface {
	ReapExpiredRuns(ctx context.Context, before time.Time, limit int) ([]storage.ExpiredRun, error)
	CountExpiredRuns(ctx context.Context, before time.Time) (int, error)
}

// Config controls the worker. All zero values are valid; defaults
// apply.
type Config struct {
	// Interval between sweeps. Defaults to DefaultInterval.
	Interval time.Duration

	// BatchSize caps each sweep's row count. Defaults to
	// DefaultBatchSize.
	BatchSize int

	// Now is a clock injection point for tests. Production callers
	// leave this nil; time.Now is used.
	Now func() time.Time
}

// Worker sweeps expired runs from DB + S3 on a periodic schedule.
type Worker struct {
	db     DB
	s3     S3
	logger *slog.Logger
	cfg    Config

	// Stats fields are atomic so /metrics (Slice 8) can read them
	// without holding a lock.
	sweepsCompleted atomic.Int64
	rowsDeleted     atomic.Int64
	rowsDBFailures  atomic.Int64
	rowsS3Failures  atomic.Int64
	lastRunUnix     atomic.Int64
}

// New constructs a Worker. The returned worker is idle until Run is
// invoked.
func New(db DB, s3 S3, logger *slog.Logger, cfg Config) *Worker {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Worker{
		db:     db,
		s3:     s3,
		logger: logger.With("subsystem", "retention"),
		cfg:    cfg,
	}
}

// Run scans for expired runs on each tick of cfg.Interval until ctx
// is cancelled. The first sweep happens immediately so a freshly
// deployed process catches up on its TTL backlog without waiting an
// hour.
//
// Run blocks. Callers typically invoke it in a dedicated goroutine
// from cmd/relic-api/main.go.
func (w *Worker) Run(ctx context.Context) {
	w.logger.Info("retention worker started",
		"interval", w.cfg.Interval,
		"batch_size", w.cfg.BatchSize,
	)
	// Immediate kick — see godoc above.
	w.sweep(ctx)
	ticker := time.NewTicker(w.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			w.logger.Info("retention worker stopping")
			return
		case <-ticker.C:
			w.sweep(ctx)
		}
	}
}

// sweep performs one retention pass. Errors are logged + counted but
// never returned — the periodic ticker swallows them and tries again
// next interval.
func (w *Worker) sweep(ctx context.Context) {
	defer w.sweepsCompleted.Add(1)
	w.lastRunUnix.Store(w.cfg.Now().Unix())

	cutoff := w.cfg.Now()
	expired, err := w.db.ReapExpiredRuns(ctx, cutoff, w.cfg.BatchSize)
	if err != nil {
		w.logger.Error("reap query failed", "error", err)
		w.rowsDBFailures.Add(1)
		return
	}
	if len(expired) == 0 {
		return
	}

	// S3 deletes run sequentially. The point of retention is "happen
	// eventually" not "happen fast"; serial keeps the implementation
	// simple and avoids fan-out failure modes (one bad key blocking
	// other deletes via a shared error channel, etc).
	var (
		wg       sync.WaitGroup
		s3Errors atomic.Int64
	)
	for _, run := range expired {
		if run.StorageKey == "" {
			// Legacy rows can have an empty storage_key — nothing to
			// delete from S3, just record the DB reap.
			continue
		}
		wg.Add(1)
		go func(key, runID string) {
			defer wg.Done()
			if err := w.s3.Delete(ctx, key); err != nil {
				w.logger.Warn("s3 delete failed during retention",
					"error", err,
					"key", key,
					"run_id", runID,
				)
				s3Errors.Add(1)
			}
		}(run.StorageKey, run.ID)
	}
	wg.Wait()
	w.rowsS3Failures.Add(s3Errors.Load())
	w.rowsDeleted.Add(int64(len(expired)))
	w.logger.Info("retention sweep completed",
		"reaped", len(expired),
		"s3_errors", s3Errors.Load(),
	)
}

// Stats is a snapshot of the worker's counters. Safe to call from
// any goroutine.
type Stats struct {
	SweepsCompleted int64
	RowsDeleted     int64
	RowsDBFailures  int64
	RowsS3Failures  int64
	LastRunAt       time.Time
}

// Stats returns the worker's counter snapshot. Read by /readyz and
// /metrics in Slice 8.
func (w *Worker) Stats() Stats {
	last := w.lastRunUnix.Load()
	var lastT time.Time
	if last > 0 {
		lastT = time.Unix(last, 0).UTC()
	}
	return Stats{
		SweepsCompleted: w.sweepsCompleted.Load(),
		RowsDeleted:     w.rowsDeleted.Load(),
		RowsDBFailures:  w.rowsDBFailures.Load(),
		RowsS3Failures:  w.rowsS3Failures.Load(),
		LastRunAt:       lastT,
	}
}
