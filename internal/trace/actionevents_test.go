package trace

import (
	"bytes"
	"testing"
)

func TestExtractActionEvents_FiltersByType(t *testing.T) {
	body := gzipNDJSON(
		`{"t":"run","status":"start","run":"r1","agent":"a","policy":"deadbeef","ts":"2025-01-01T00:00:00Z"}`,
		`{"t":"action","run":"r1","proto":"mcp","method":"tool_call","target":"web_search","auth":"allow","seq":1}`,
		`{"t":"policy_reload","run":"r1","policy":".tr/policy.yaml","status":"ok","ts":"2025-01-01T00:00:01Z"}`,
		`{"t":"action","run":"r1","proto":"mcp","method":"tool_call","target":"exec_shell","auth":"deny","seq":2}`,
		`{"t":"run","status":"end","run":"r1","exit":0,"ms":1000}`,
	)

	events, err := ExtractActionEvents(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Target != "web_search" || events[0].OldAuth != "allow" {
		t.Errorf("event 0: %+v", events[0])
	}
	if events[1].Target != "exec_shell" || events[1].OldAuth != "deny" {
		t.Errorf("event 1: %+v", events[1])
	}
}

func TestExtractActionEvents_SkipsMalformedLines(t *testing.T) {
	body := gzipNDJSON(
		`{"t":"action","run":"r1","proto":"mcp","method":"tool_call","target":"a","auth":"allow"}`,
		`not-json`,
		`{"t":"action","run":"r1","proto":"mcp","method":"tool_call","target":"b","auth":"deny"}`,
	)

	events, err := ExtractActionEvents(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
}

func TestExtractActionEvents_PreservesParams(t *testing.T) {
	body := gzipNDJSON(
		`{"t":"action","run":"r1","proto":"mcp","method":"tool_call","target":"web_fetch","auth":"allow","params":{"url":"https://x.com"}}`,
	)

	events, err := ExtractActionEvents(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if string(events[0].Params) != `{"url":"https://x.com"}` {
		t.Errorf("params raw: %q", string(events[0].Params))
	}
}
