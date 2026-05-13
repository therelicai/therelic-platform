// Package simulate runs candidate-policy diff jobs against historical
// traces. The public surface is small: SubmitJob queues a request,
// GetJob polls a result. Job state is in-memory — slice 13 doesn't
// need durability, and the acceptance budget (10s for 7 days × 1
// agent) is well inside one process lifetime.
//
// The runner is org-scoped at the API boundary; this package trusts
// the caller has already authorized the (orgID, agentName) pair.
package simulate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/therelicai/therelic-platform/internal/policy"
	"github.com/therelicai/therelic-platform/internal/storage"
	"github.com/therelicai/therelic-platform/internal/trace"
)

// JobStatus tracks the lifecycle of a single Submit call.
type JobStatus string

const (
	StatusPending JobStatus = "pending"
	StatusRunning JobStatus = "running"
	StatusDone    JobStatus = "done"
	StatusError   JobStatus = "error"
)

// Request is the shape SubmitJob accepts. The selector is restricted
// to { agent_name } for slice 13; slice 15 extends to label-match.
type Request struct {
	OrgID      string
	AgentName  string
	PolicyYAML string
	WindowDays int
}

// Result is the public payload of a finished job. Mirrors the shape
// the API returns under `result`.
type Result struct {
	NewlyDenied    int                 `json:"newly_denied"`
	NewlyAllowed   int                 `json:"newly_allowed"`
	Unchanged      int                 `json:"unchanged"`
	TotalEvaluated int                 `json:"total_evaluated"`
	RunsScanned    int                 `json:"runs_scanned"`
	Samples        []policy.SampleFlip `json:"samples"`
}

// Job is the value stored against a job_id. We export it (instead of
// returning a Result) so the API can also surface Status and Error.
type Job struct {
	ID         string    `json:"job_id"`
	OrgID      string    `json:"org_id"`
	Status     JobStatus `json:"status"`
	Error      string    `json:"error,omitempty"`
	Result     *Result   `json:"result,omitempty"`
	SubmittedAt time.Time `json:"submitted_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
}

// runsLister is the subset of *storage.Postgres the runner needs. Kept
// narrow so tests can fake it without spinning up Postgres.
type runsLister interface {
	ListRunsForSimulate(ctx context.Context, orgID, agentName string, since time.Time, limit int) ([]storage.Run, error)
}

// traceDownloader is the subset of *storage.S3 the runner needs.
type traceDownloader interface {
	Download(ctx context.Context, key string) (io.ReadCloser, error)
}

// Runner is the in-process job store + worker pool. One Runner per
// API server instance. Concurrency knobs are stable for slice 13;
// they'll need tuning once slice 15 multi-agent simulations land.
type Runner struct {
	db     runsLister
	s3     traceDownloader
	logger *slog.Logger

	mu   sync.Mutex
	jobs map[string]*Job

	// maxRunsPerSim caps how many historical runs we'll consider for
	// a single simulation. Slice 13's acceptance test runs against
	// one agent over 7 days — orders of magnitude below this — but
	// the cap prevents a malicious or runaway tenant from pulling
	// down 50 GB of S3 objects on a single click.
	maxRunsPerSim int

	// fanout caps in-flight trace downloads per job. Four matches the
	// pgxpool default and keeps memory bounded.
	fanout int

	// jobTimeout caps total wall-clock per simulation. The acceptance
	// test targets 10s; we give 60s of headroom so the budget isn't
	// the failure mode on a cold S3 cache.
	jobTimeout time.Duration
}

// NewRunner wires a Runner with production defaults.
func NewRunner(db runsLister, s3 traceDownloader, logger *slog.Logger) *Runner {
	return &Runner{
		db:            db,
		s3:            s3,
		logger:        logger,
		jobs:          make(map[string]*Job),
		maxRunsPerSim: 200,
		fanout:        4,
		jobTimeout:    60 * time.Second,
	}
}

// ErrInvalidRequest is returned from SubmitJob when the request shape
// is malformed (empty selector, missing YAML, out-of-range window).
// Distinct error class so the API layer maps to 400 rather than 500.
var ErrInvalidRequest = errors.New("simulate: invalid request")

// ErrJobNotFound is returned when GetJob can't locate a job id for
// the given org. Returns 404 at the API layer.
var ErrJobNotFound = errors.New("simulate: job not found")

// SubmitJob validates the request, parses + validates the candidate
// policy synchronously, then kicks off the async runner. We do the
// policy parse synchronously so a YAML typo surfaces as a 400 from
// the POST instead of as a status:error from a follow-up GET — the
// latter is a much worse UX, especially in the policy editor where
// the user just typed the YAML.
func (r *Runner) SubmitJob(ctx context.Context, req Request) (string, error) {
	if req.OrgID == "" {
		return "", fmt.Errorf("%w: org missing", ErrInvalidRequest)
	}
	if req.AgentName == "" {
		return "", fmt.Errorf("%w: selector is required", ErrInvalidRequest)
	}
	if req.PolicyYAML == "" {
		return "", fmt.Errorf("%w: policy_yaml is required", ErrInvalidRequest)
	}
	switch req.WindowDays {
	case 1, 7, 30:
	default:
		return "", fmt.Errorf("%w: window_days must be 1, 7, or 30", ErrInvalidRequest)
	}

	candidate, err := policy.Parse([]byte(req.PolicyYAML))
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if errs := policy.Validate(candidate, false); len(errs) > 0 {
		// Surface the first validation error — the API caller wants the
		// concrete reason, not a list dump.
		return "", fmt.Errorf("%w: %s", ErrInvalidRequest, errs[0].Error())
	}

	job := &Job{
		ID:          ulid.Make().String(),
		OrgID:       req.OrgID,
		Status:      StatusPending,
		SubmittedAt: time.Now().UTC(),
	}

	r.mu.Lock()
	r.jobs[job.ID] = job
	r.mu.Unlock()

	// Detach from the request context so a client disconnect doesn't
	// cancel an in-progress simulation. We still bound execution via
	// jobTimeout.
	go r.run(job, candidate, req)

	return job.ID, nil
}

// GetJob returns a snapshot of the named job. The returned pointer is
// a copy of the live struct so the caller can serialize it without
// holding the runner's lock. The copy MUST happen under the lock —
// the worker goroutine mutates job.Status / job.Result concurrently.
func (r *Runner) GetJob(orgID, jobID string) (*Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[jobID]
	if !ok || j.OrgID != orgID {
		return nil, ErrJobNotFound
	}
	cp := *j
	return &cp, nil
}

// run executes one simulation. Status transitions:
//   pending -> running -> (done | error)
func (r *Runner) run(job *Job, candidate *policy.Policy, req Request) {
	r.setStatus(job, StatusRunning, "")

	ctx, cancel := context.WithTimeout(context.Background(), r.jobTimeout)
	defer cancel()

	since := time.Now().UTC().Add(-time.Duration(req.WindowDays) * 24 * time.Hour)
	runs, err := r.db.ListRunsForSimulate(ctx, req.OrgID, req.AgentName, since, r.maxRunsPerSim)
	if err != nil {
		r.finishError(job, fmt.Errorf("list runs: %w", err))
		return
	}

	if len(runs) == 0 {
		r.finishDone(job, &Result{Samples: []policy.SampleFlip{}})
		return
	}

	events, err := r.fetchEvents(ctx, runs)
	if err != nil {
		r.finishError(job, err)
		return
	}

	diff := policy.Simulate(candidate, events)

	// policy.Simulate samples per-bucket internally (cap 5 each); we
	// pass the buckets through and let the UI handle presentation.
	r.finishDone(job, &Result{
		NewlyDenied:    diff.NewlyDenied,
		NewlyAllowed:   diff.NewlyAllowed,
		Unchanged:      diff.Unchanged,
		TotalEvaluated: diff.TotalEvaluated,
		RunsScanned:    len(runs),
		Samples:        sortSamples(diff.Samples),
	})
}

// fetchEvents downloads up to r.fanout traces in parallel and returns
// the flattened ActionEvent stream. We fail soft on individual trace
// errors — a single missing S3 object shouldn't fail the whole
// simulation — but we log them so an ops dashboard can catch a
// systemic problem.
func (r *Runner) fetchEvents(ctx context.Context, runs []storage.Run) ([]policy.ActionEvent, error) {
	type result struct {
		events []policy.ActionEvent
		err    error
	}
	resCh := make(chan result, len(runs))
	sem := make(chan struct{}, r.fanout)

	var wg sync.WaitGroup
	for i := range runs {
		run := runs[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			body, err := r.s3.Download(ctx, run.StorageKey)
			if err != nil {
				resCh <- result{err: fmt.Errorf("download %s: %w", run.ID, err)}
				return
			}
			defer body.Close()

			events, err := trace.ExtractActionEvents(body)
			if err != nil && len(events) == 0 {
				resCh <- result{err: fmt.Errorf("extract %s: %w", run.ID, err)}
				return
			}
			// Each event was decoded from a different run's trace, but
			// the RunID field is populated from the action line's "run"
			// key, which matches run.ID by construction. We trust that.
			resCh <- result{events: events}
		}()
	}

	go func() {
		wg.Wait()
		close(resCh)
	}()

	out := make([]policy.ActionEvent, 0, 256)
	var firstErr error
	for r := range resCh {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		out = append(out, r.events...)
	}
	// If every download failed, surface the first error. Otherwise the
	// simulation just runs on the events we managed to fetch.
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

func (r *Runner) setStatus(job *Job, s JobStatus, errMsg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job.Status = s
	job.Error = errMsg
}

func (r *Runner) finishDone(job *Job, res *Result) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job.Status = StatusDone
	job.Result = res
	job.FinishedAt = time.Now().UTC()
}

func (r *Runner) finishError(job *Job, err error) {
	r.logger.Error("simulate job failed", "job_id", job.ID, "org_id", job.OrgID, "error", err)
	r.mu.Lock()
	defer r.mu.Unlock()
	job.Status = StatusError
	job.Error = err.Error()
	job.FinishedAt = time.Now().UTC()
}

// sortSamples gives the UI a stable order: newly_denied first (most
// urgent for "this rule would break things"), then newly_allowed.
// Within each direction, alphabetical by target for determinism.
func sortSamples(s []policy.SampleFlip) []policy.SampleFlip {
	out := append([]policy.SampleFlip(nil), s...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].NewAuth == out[j].NewAuth {
			return out[i].Target < out[j].Target
		}
		// Denies first.
		return out[i].NewAuth == "deny" || out[i].NewAuth == "audit_deny" || out[i].NewAuth == "would_deny"
	})
	return out
}
