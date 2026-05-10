package trace

import (
	"bytes"
	"compress/gzip"
	"errors"
	"strings"
	"testing"
)

// gzipNDJSON compresses each line as gzipped NDJSON, the format the
// runtime produces. Used to construct fixtures in-place.
func gzipNDJSON(lines ...string) []byte {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	for _, line := range lines {
		if _, err := w.Write([]byte(line + "\n")); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func TestParse_HappyPath(t *testing.T) {
	body := gzipNDJSON(
		`{"v":1,"t":"run","ts":"2026-05-10T20:00:00Z","run":"01H...","agent":"code-assist","agent_v":"1.2","policy":"sha:abc","env":"prod","status":"start"}`,
		`{"v":1,"t":"action","ts":"2026-05-10T20:00:01Z","run":"01H...","seq":1,"proto":"mcp","method":"tool_call","target":"read_file","auth":"allow","rule":"default"}`,
		`{"v":1,"t":"action","ts":"2026-05-10T20:00:02Z","run":"01H...","seq":2,"proto":"mcp","method":"tool_call","target":"execute_command","auth":"deny","rule":"deny-shell"}`,
		`{"v":1,"t":"action","ts":"2026-05-10T20:00:03Z","run":"01H...","seq":3,"proto":"mcp","method":"tool_call","target":"write_file","auth":"audit_deny","rule":"audit-write"}`,
		`{"v":1,"t":"run","ts":"2026-05-10T20:00:04Z","run":"01H...","status":"end","exit":0,"ms":4000,"actions_total":3,"actions_allowed":1,"actions_denied":2}`,
	)

	s, err := Parse(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	checks := map[string]any{
		"RunID":          "01H...",
		"AgentName":      "code-assist",
		"AgentVersion":   "1.2",
		"PolicyHash":     "sha:abc",
		"Environment":    "prod",
		"ActionsTotal":   3,
		"ActionsAllowed": 1, // allow
		"ActionsDenied":  2, // deny + audit_deny
		"EventCount":     5,
	}
	for field, want := range checks {
		var got any
		switch field {
		case "RunID":
			got = s.RunID
		case "AgentName":
			got = s.AgentName
		case "AgentVersion":
			got = s.AgentVersion
		case "PolicyHash":
			got = s.PolicyHash
		case "Environment":
			got = s.Environment
		case "ActionsTotal":
			got = s.ActionsTotal
		case "ActionsAllowed":
			got = s.ActionsAllowed
		case "ActionsDenied":
			got = s.ActionsDenied
		case "EventCount":
			got = s.EventCount
		}
		if got != want {
			t.Errorf("%s: got %v, want %v", field, got, want)
		}
	}

	if s.ExitCode == nil || *s.ExitCode != 0 {
		t.Errorf("ExitCode: got %v, want 0", s.ExitCode)
	}
	if s.DurationMs == nil || *s.DurationMs != 4000 {
		t.Errorf("DurationMs: got %v, want 4000", s.DurationMs)
	}
}

func TestParse_RecomputesCountsIgnoringHeaders(t *testing.T) {
	// Run-end lies about counts. Parser must trust the action stream.
	body := gzipNDJSON(
		`{"v":1,"t":"run","run":"r1","agent":"a","status":"start","ts":"2026-05-10T20:00:00Z"}`,
		`{"v":1,"t":"action","run":"r1","seq":1,"auth":"allow"}`,
		`{"v":1,"t":"action","run":"r1","seq":2,"auth":"deny"}`,
		`{"v":1,"t":"run","run":"r1","status":"end","actions_total":9999,"actions_allowed":9999,"actions_denied":0}`,
	)
	s, err := Parse(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if s.ActionsTotal != 2 || s.ActionsAllowed != 1 || s.ActionsDenied != 1 {
		t.Errorf("counts = (%d,%d,%d), want (2,1,1) — must ignore run-end totals",
			s.ActionsTotal, s.ActionsAllowed, s.ActionsDenied)
	}
}

func TestParse_AuditAndWouldDenyCountAsDenied(t *testing.T) {
	body := gzipNDJSON(
		`{"v":1,"t":"run","run":"r1","agent":"a","status":"start"}`,
		`{"v":1,"t":"action","run":"r1","auth":"allow"}`,
		`{"v":1,"t":"action","run":"r1","auth":"deny"}`,
		`{"v":1,"t":"action","run":"r1","auth":"audit_deny"}`,
		`{"v":1,"t":"action","run":"r1","auth":"would_deny"}`,
	)
	s, err := Parse(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if s.ActionsAllowed != 1 || s.ActionsDenied != 3 {
		t.Errorf("got allowed=%d denied=%d, want 1 and 3",
			s.ActionsAllowed, s.ActionsDenied)
	}
}

func TestParse_EmptyTrace(t *testing.T) {
	body := gzipNDJSON()
	_, err := Parse(bytes.NewReader(body))
	if !errors.Is(err, ErrEmptyTrace) {
		t.Errorf("got %v, want ErrEmptyTrace", err)
	}
}

func TestParse_MissingRunStart(t *testing.T) {
	body := gzipNDJSON(
		`{"v":1,"t":"action","run":"r1","auth":"allow"}`,
	)
	_, err := Parse(bytes.NewReader(body))
	if !errors.Is(err, ErrMissingRunStart) {
		t.Errorf("got %v, want ErrMissingRunStart", err)
	}
}

func TestParse_NotGzip(t *testing.T) {
	_, err := Parse(strings.NewReader("not gzip"))
	if err == nil {
		t.Fatal("expected error for non-gzip input")
	}
	if !strings.Contains(err.Error(), "gzip") {
		t.Errorf("error %q should mention gzip", err)
	}
}

func TestParse_LineTooLong(t *testing.T) {
	huge := strings.Repeat("x", MaxEventSize+10)
	body := gzipNDJSON(
		`{"v":1,"t":"run","run":"r1","agent":"a","status":"start"}`,
		`{"v":1,"t":"action","run":"r1","auth":"allow","params":"`+huge+`"}`,
	)
	_, err := Parse(bytes.NewReader(body))
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("got %v, want ErrTooLarge", err)
	}
}

func TestParse_TolerantOfMalformedLines(t *testing.T) {
	body := gzipNDJSON(
		`{"v":1,"t":"run","run":"r1","agent":"a","status":"start"}`,
		`{not valid json`,
		`{"v":1,"t":"action","run":"r1","auth":"allow"}`,
	)
	s, err := Parse(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.ActionsTotal != 1 {
		t.Errorf("ActionsTotal = %d, want 1", s.ActionsTotal)
	}
	if !s.Truncated {
		t.Error("Truncated should be true after malformed line")
	}
}

func TestParse_HMACChainDetection(t *testing.T) {
	bodyWith := gzipNDJSON(
		`{"v":1,"t":"run","run":"r1","agent":"a","status":"start"}`,
		`{"v":1,"t":"action","run":"r1","auth":"allow","hmac":"abc"}`,
		`{"v":1,"t":"action","run":"r1","auth":"allow","hmac":"def"}`,
	)
	s, err := Parse(bytes.NewReader(bodyWith))
	if err != nil {
		t.Fatal(err)
	}
	if !s.HasIntegrityChain {
		t.Error("HasIntegrityChain should be true when every action has hmac")
	}

	bodyMixed := gzipNDJSON(
		`{"v":1,"t":"run","run":"r1","agent":"a","status":"start"}`,
		`{"v":1,"t":"action","run":"r1","auth":"allow","hmac":"abc"}`,
		`{"v":1,"t":"action","run":"r1","auth":"allow"}`,
	)
	s2, err := Parse(bytes.NewReader(bodyMixed))
	if err != nil {
		t.Fatal(err)
	}
	if s2.HasIntegrityChain {
		t.Error("HasIntegrityChain should be false when any action lacks hmac")
	}

	bodyNone := gzipNDJSON(
		`{"v":1,"t":"run","run":"r1","agent":"a","status":"start"}`,
	)
	s3, err := Parse(bytes.NewReader(bodyNone))
	if err != nil {
		t.Fatal(err)
	}
	if s3.HasIntegrityChain {
		t.Error("HasIntegrityChain should be false when no actions seen")
	}
}

func TestParse_StartTimestampExtracted(t *testing.T) {
	body := gzipNDJSON(
		`{"v":1,"t":"run","run":"r1","agent":"a","status":"start","ts":"2026-05-10T20:00:00.123456789Z"}`,
		`{"v":1,"t":"action","run":"r1","auth":"allow"}`,
	)
	s, err := Parse(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if s.StartedAt.Year() != 2026 || s.StartedAt.Nanosecond() == 0 {
		t.Errorf("StartedAt = %v, want 2026-05-10T20:00:00.123456789Z", s.StartedAt)
	}
}
