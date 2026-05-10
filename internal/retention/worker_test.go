package retention

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/therelicai/therelic-platform/internal/storage"
)

type fakeDB struct {
	mu      sync.Mutex
	rows    []storage.ExpiredRun
	reapErr error
	calls   int
}

func (f *fakeDB) ReapExpiredRuns(_ context.Context, _ time.Time, limit int) ([]storage.ExpiredRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.reapErr != nil {
		return nil, f.reapErr
	}
	if limit >= len(f.rows) {
		out := f.rows
		f.rows = nil
		return out, nil
	}
	out := f.rows[:limit]
	f.rows = f.rows[limit:]
	return out, nil
}

func (f *fakeDB) CountExpiredRuns(_ context.Context, _ time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows), nil
}

type fakeS3 struct {
	mu      sync.Mutex
	deleted []string
	failOn  map[string]bool
}

func (f *fakeS3) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failOn[key] {
		return errors.New("simulated s3 failure")
	}
	f.deleted = append(f.deleted, key)
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestWorker_SweepDeletesFromDBAndS3(t *testing.T) {
	db := &fakeDB{rows: []storage.ExpiredRun{
		{ID: "r1", OrgID: "o1", StorageKey: "o1/agent/r1.trtrace.gz"},
		{ID: "r2", OrgID: "o1", StorageKey: "o1/agent/r2.trtrace.gz"},
	}}
	s3 := &fakeS3{}
	w := New(db, s3, discardLogger(), Config{BatchSize: 10})
	w.sweep(context.Background())

	if len(s3.deleted) != 2 {
		t.Fatalf("expected 2 S3 deletes, got %d", len(s3.deleted))
	}
	if got := w.Stats().RowsDeleted; got != 2 {
		t.Errorf("RowsDeleted = %d, want 2", got)
	}
	if got := w.Stats().RowsS3Failures; got != 0 {
		t.Errorf("RowsS3Failures = %d, want 0", got)
	}
}

func TestWorker_S3FailureDoesNotBlockProgress(t *testing.T) {
	db := &fakeDB{rows: []storage.ExpiredRun{
		{ID: "r1", OrgID: "o1", StorageKey: "bad/key"},
		{ID: "r2", OrgID: "o1", StorageKey: "good/key"},
	}}
	s3 := &fakeS3{failOn: map[string]bool{"bad/key": true}}
	w := New(db, s3, discardLogger(), Config{BatchSize: 10})
	w.sweep(context.Background())

	if got := w.Stats().RowsS3Failures; got != 1 {
		t.Errorf("RowsS3Failures = %d, want 1", got)
	}
	// We still count both rows as DB-reaped because the DELETE already
	// happened — S3 cleanup is best-effort.
	if got := w.Stats().RowsDeleted; got != 2 {
		t.Errorf("RowsDeleted = %d, want 2 (DB delete always counted)", got)
	}
}

func TestWorker_EmptyDBDoesNotPanic(t *testing.T) {
	db := &fakeDB{}
	s3 := &fakeS3{}
	w := New(db, s3, discardLogger(), Config{BatchSize: 10})
	w.sweep(context.Background())
	if got := w.Stats().RowsDeleted; got != 0 {
		t.Errorf("RowsDeleted = %d, want 0", got)
	}
}

func TestWorker_DBErrorDoesNotPanic(t *testing.T) {
	db := &fakeDB{reapErr: errors.New("db broke")}
	s3 := &fakeS3{}
	w := New(db, s3, discardLogger(), Config{BatchSize: 10})
	w.sweep(context.Background())
	if got := w.Stats().RowsDBFailures; got != 1 {
		t.Errorf("RowsDBFailures = %d, want 1", got)
	}
}

func TestWorker_LegacyEmptyStorageKey(t *testing.T) {
	// Rows from before we required storage_key on insert. The worker
	// should reap them without trying an S3 delete of "".
	db := &fakeDB{rows: []storage.ExpiredRun{
		{ID: "legacy", OrgID: "o1", StorageKey: ""},
	}}
	s3 := &fakeS3{}
	w := New(db, s3, discardLogger(), Config{BatchSize: 10})
	w.sweep(context.Background())
	if len(s3.deleted) != 0 {
		t.Errorf("expected 0 S3 deletes for legacy row, got %d", len(s3.deleted))
	}
	if got := w.Stats().RowsDeleted; got != 1 {
		t.Errorf("RowsDeleted = %d, want 1", got)
	}
}

func TestWorker_RunStopsOnContextCancel(t *testing.T) {
	db := &fakeDB{}
	s3 := &fakeS3{}
	w := New(db, s3, discardLogger(), Config{Interval: 10 * time.Millisecond, BatchSize: 10})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(done)
	}()

	// Let at least one tick happen so we know Run started.
	time.Sleep(25 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not exit within 1s of cancel")
	}

	// First sweep is immediate, plus at least one timer tick.
	if got := w.Stats().SweepsCompleted; got < 1 {
		t.Errorf("SweepsCompleted = %d, want >= 1", got)
	}
}

func TestWorker_DefaultsApplied(t *testing.T) {
	w := New(&fakeDB{}, &fakeS3{}, discardLogger(), Config{})
	if w.cfg.Interval != DefaultInterval {
		t.Errorf("Interval = %v, want %v", w.cfg.Interval, DefaultInterval)
	}
	if w.cfg.BatchSize != DefaultBatchSize {
		t.Errorf("BatchSize = %d, want %d", w.cfg.BatchSize, DefaultBatchSize)
	}
	if w.cfg.Now == nil {
		t.Error("Now should be defaulted to time.Now")
	}
}
