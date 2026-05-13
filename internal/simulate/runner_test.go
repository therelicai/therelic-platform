package simulate

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/therelicai/therelic-platform/internal/storage"
)

// fakeDB returns a fixed run list. Slice 13's job runner doesn't care
// about anything beyond ID + StorageKey + AgentName, so the rest of
// the Run is left zero-valued.
type fakeDB struct {
	runs []storage.Run
	err  error
}

func (f *fakeDB) ListRunsForSimulate(_ context.Context, _, _ string, _ time.Time, _ int) ([]storage.Run, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.runs, nil
}

// fakeS3 maps storage keys -> raw NDJSON bytes, gzipping on the fly.
// Each test names the keys it returns so a missing key surfaces a
// real error path through the runner's fail-soft download logic.
type fakeS3 struct {
	keys map[string]string
}

func (f *fakeS3) Download(_ context.Context, key string) (io.ReadCloser, error) {
	raw, ok := f.keys[key]
	if !ok {
		return nil, errors.New("not found")
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(raw)); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return io.NopCloser(&buf), nil
}

// waitForStatus polls the runner until the job reaches a terminal
// state or the timeout expires. Slice 13's worker is in-process and
// finishes in millseconds for small fixtures, so 2s is generous.
func waitForStatus(t *testing.T, r *Runner, orgID, jobID string) *Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		j, err := r.GetJob(orgID, jobID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if j.Status == StatusDone || j.Status == StatusError {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s did not finish in time", jobID)
	return nil
}

const candidateYAML = `version: "1"
agent:
  name: simulator-test
mode: enforce
default: deny
rules:
  - id: allow-search
    protocol: mcp
    method: tool_call
    target: web_search
    action: allow
`

func TestRunner_LifecycleProducesDiff(t *testing.T) {
	traceNDJSON := strings.Join([]string{
		`{"t":"run","status":"start","run":"r1","agent":"a","ts":"2025-01-01T00:00:00Z"}`,
		`{"t":"action","run":"r1","proto":"mcp","method":"tool_call","target":"web_search","auth":"allow","seq":1}`,
		`{"t":"action","run":"r1","proto":"mcp","method":"tool_call","target":"exec_shell","auth":"allow","seq":2}`,
		`{"t":"run","status":"end","run":"r1","exit":0,"ms":100}`,
		"",
	}, "\n")

	db := &fakeDB{runs: []storage.Run{
		{ID: "r1", OrgID: "org-A", AgentName: "agent-1", StorageKey: "k1"},
	}}
	s3 := &fakeS3{keys: map[string]string{"k1": traceNDJSON}}

	r := NewRunner(db, s3, slog.Default())
	jobID, err := r.SubmitJob(context.Background(), Request{
		OrgID:      "org-A",
		AgentName:  "agent-1",
		PolicyYAML: candidateYAML,
		WindowDays: 7,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	j := waitForStatus(t, r, "org-A", jobID)
	if j.Status != StatusDone {
		t.Fatalf("status: got %s (%s), want done", j.Status, j.Error)
	}
	if j.Result == nil {
		t.Fatal("result is nil")
	}
	if j.Result.RunsScanned != 1 {
		t.Errorf("RunsScanned: got %d, want 1", j.Result.RunsScanned)
	}
	if j.Result.TotalEvaluated != 2 {
		t.Errorf("TotalEvaluated: got %d, want 2", j.Result.TotalEvaluated)
	}
	if j.Result.NewlyDenied != 1 {
		t.Errorf("NewlyDenied: got %d, want 1 (exec_shell flips allow -> deny)", j.Result.NewlyDenied)
	}
	if j.Result.Unchanged != 1 {
		t.Errorf("Unchanged: got %d, want 1 (web_search stays allow)", j.Result.Unchanged)
	}
}

func TestRunner_RejectsEmptySelector(t *testing.T) {
	r := NewRunner(&fakeDB{}, &fakeS3{}, slog.Default())
	_, err := r.SubmitJob(context.Background(), Request{
		OrgID:      "org-A",
		AgentName:  "",
		PolicyYAML: candidateYAML,
		WindowDays: 7,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("got %v, want ErrInvalidRequest", err)
	}
}

func TestRunner_RejectsInvalidPolicyYAML(t *testing.T) {
	r := NewRunner(&fakeDB{}, &fakeS3{}, slog.Default())
	_, err := r.SubmitJob(context.Background(), Request{
		OrgID:      "org-A",
		AgentName:  "agent-1",
		PolicyYAML: "version: 1\nmode: enforce", // missing agent.name, default
		WindowDays: 7,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("got %v, want ErrInvalidRequest", err)
	}
}

func TestRunner_RejectsInvalidWindow(t *testing.T) {
	r := NewRunner(&fakeDB{}, &fakeS3{}, slog.Default())
	_, err := r.SubmitJob(context.Background(), Request{
		OrgID:      "org-A",
		AgentName:  "agent-1",
		PolicyYAML: candidateYAML,
		WindowDays: 5,
	})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("got %v, want ErrInvalidRequest", err)
	}
}

func TestRunner_NoRunsReturnsEmptyResult(t *testing.T) {
	r := NewRunner(&fakeDB{runs: nil}, &fakeS3{}, slog.Default())
	jobID, err := r.SubmitJob(context.Background(), Request{
		OrgID:      "org-A",
		AgentName:  "agent-1",
		PolicyYAML: candidateYAML,
		WindowDays: 30,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	j := waitForStatus(t, r, "org-A", jobID)
	if j.Status != StatusDone {
		t.Fatalf("status: %s", j.Status)
	}
	if j.Result.TotalEvaluated != 0 || j.Result.RunsScanned != 0 {
		t.Errorf("expected empty result, got %+v", j.Result)
	}
}

func TestRunner_OrgScopingPreventsCrossTenantPolling(t *testing.T) {
	r := NewRunner(&fakeDB{}, &fakeS3{}, slog.Default())
	jobID, err := r.SubmitJob(context.Background(), Request{
		OrgID:      "org-A",
		AgentName:  "agent-1",
		PolicyYAML: candidateYAML,
		WindowDays: 1,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	_, err = r.GetJob("org-B", jobID)
	if !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("got %v, want ErrJobNotFound for cross-tenant poll", err)
	}
}
