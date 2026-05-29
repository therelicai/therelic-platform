package governance

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// Pure-logic tests for the governance package. The Worker / Proposer
// / Detector paths that touch *storage.Postgres + *storage.S3 are
// covered by integration tests; here we focus on the units that
// previously had no coverage and that govern correctness of the
// proposal-generation pipeline.

func TestTraceActionEvent_toolName_prefersTargetOverTool(t *testing.T) {
	ev := traceActionEvent{Target: "fs.read", Tool: "ignored"}
	if got := ev.toolName(); got != "fs.read" {
		t.Errorf("toolName=%q; want fs.read (Target should win)", got)
	}
}

func TestTraceActionEvent_toolName_fallsBackToTool(t *testing.T) {
	ev := traceActionEvent{Target: "", Tool: "shell.exec"}
	if got := ev.toolName(); got != "shell.exec" {
		t.Errorf("toolName=%q; want shell.exec (Tool fallback)", got)
	}
}

func TestTraceActionEvent_toolName_emptyWhenNeitherSet(t *testing.T) {
	ev := traceActionEvent{}
	if got := ev.toolName(); got != "" {
		t.Errorf("toolName=%q; want empty string", got)
	}
}

func TestTraceActionEvent_isAction_acceptsBothSchemas(t *testing.T) {
	// Both the runtime's `"t":"action"` and the alternate
	// `"type":"action"` schema should be recognized. A regression
	// here silently drops half the trace stream.
	if !(traceActionEvent{Type: "action"}).isAction() {
		t.Error(`"t":"action" not recognized`)
	}
	if !(traceActionEvent{TypeAlt: "action"}).isAction() {
		t.Error(`"type":"action" not recognized`)
	}
	if (traceActionEvent{Type: "run"}).isAction() {
		t.Error(`"t":"run" wrongly classified as action`)
	}
}

func TestTraceActionEvent_isDenied_exactMatch(t *testing.T) {
	if !(traceActionEvent{Auth: "deny"}).isDenied() {
		t.Error(`auth="deny" not recognized`)
	}
	// Case sensitivity matters — the runtime always emits lower-case.
	// "Deny" would mean a schema drift bug worth catching.
	if (traceActionEvent{Auth: "Deny"}).isDenied() {
		t.Error(`auth="Deny" matched; runtime emits lower-case only`)
	}
	if (traceActionEvent{Auth: "allow"}).isDenied() {
		t.Error(`auth="allow" matched`)
	}
}

func TestParseDeniedToolsNDJSON_countsAndSamples(t *testing.T) {
	stream := `{"t":"run","run":"r1","status":"start"}
{"t":"action","target":"fs.read","auth":"allow"}
{"t":"action","target":"fs.write","auth":"deny","params":{"path":"/etc/passwd"}}
{"t":"action","target":"fs.write","auth":"deny","params":{"path":"/root/.ssh"}}
{"t":"action","target":"net.fetch","auth":"deny"}
`
	counts, sample, err := parseDeniedToolsNDJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if counts["fs.write"] != 2 {
		t.Errorf("fs.write count=%d; want 2", counts["fs.write"])
	}
	if counts["net.fetch"] != 1 {
		t.Errorf("net.fetch count=%d; want 1", counts["net.fetch"])
	}
	if _, ok := counts["fs.read"]; ok {
		t.Error("fs.read counted; allow events must not be counted")
	}
	if !strings.Contains(sample, "/etc/passwd") {
		t.Errorf("sample=%q; should include first denied params", sample)
	}
}

func TestParseDeniedToolsNDJSON_skipsMalformedLines(t *testing.T) {
	stream := `{"t":"action","target":"shell.exec","auth":"deny"}
not-json-at-all
{"t":"action","target":"shell.exec","auth":"deny"}
{ broken json
{"t":"action","target":"shell.exec","auth":"deny"}
`
	counts, _, err := parseDeniedToolsNDJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parse should not error on malformed lines: %v", err)
	}
	if counts["shell.exec"] != 3 {
		t.Errorf("shell.exec count=%d; want 3 (malformed lines should be skipped, not crash)", counts["shell.exec"])
	}
}

func TestParseDeniedToolsNDJSON_truncatesLargeSample(t *testing.T) {
	// Build a denied action with > 500 bytes of params. The function
	// must truncate the sample to keep proposal payloads bounded.
	bigParams := strings.Repeat("x", 800)
	line := `{"t":"action","target":"fs.write","auth":"deny","params":"` + bigParams + `"}` + "\n"
	_, sample, err := parseDeniedToolsNDJSON(strings.NewReader(line))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(sample) > 504 { // 500 chars + "..." suffix
		t.Errorf("sample length=%d; want <=503 (truncation contract)", len(sample))
	}
	if !strings.HasSuffix(sample, "...") {
		t.Errorf("sample=%q; want trailing ... when truncated", sample)
	}
}

func TestParseDeniedToolsNDJSON_handlesAlternateSchema(t *testing.T) {
	// Some traces use `type:"action"` / `tool:"..."` instead of
	// `t:"action"` / `target:"..."`. Both must count.
	stream := `{"type":"action","tool":"fs.delete","auth":"deny"}
{"type":"action","tool":"fs.delete","auth":"deny"}
`
	counts, _, err := parseDeniedToolsNDJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if counts["fs.delete"] != 2 {
		t.Errorf("fs.delete count=%d via alt schema; want 2", counts["fs.delete"])
	}
}

func TestParseDeniedToolsNDJSON_assignsUnknownWhenToolMissing(t *testing.T) {
	// Action denied with no target/tool field at all. Should be
	// bucketed under "unknown" rather than dropped silently.
	stream := `{"t":"action","auth":"deny"}` + "\n"
	counts, _, err := parseDeniedToolsNDJSON(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if counts["unknown"] != 1 {
		t.Errorf("unknown count=%d; want 1", counts["unknown"])
	}
}

func TestParseDeniedToolsNDJSON_emptyStream(t *testing.T) {
	counts, sample, err := parseDeniedToolsNDJSON(strings.NewReader(""))
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if len(counts) != 0 {
		t.Errorf("counts=%v on empty stream; want empty map", counts)
	}
	if sample != "" {
		t.Errorf("sample=%q on empty stream; want empty string", sample)
	}
}

func TestDetector_GenerateProposal_shape(t *testing.T) {
	d := &Detector{}
	p := d.GenerateProposal("org-1", DenialPattern{
		AgentName:   "scout",
		Tool:        "fs.read",
		Count:       7,
		RunIDs:      []string{"r1", "r2"},
		SampleParam: `{"path":"/etc/hosts"}`,
	})
	if p.OrgID != "org-1" {
		t.Errorf("OrgID=%q; want org-1", p.OrgID)
	}
	if p.AgentName != "scout" {
		t.Errorf("AgentName=%q; want scout", p.AgentName)
	}
	if p.TriggerType != "denial_pattern" {
		t.Errorf("TriggerType=%q; want denial_pattern", p.TriggerType)
	}

	var rule map[string]any
	if err := json.Unmarshal(p.ProposedRule, &rule); err != nil {
		t.Fatalf("ProposedRule not valid JSON: %v", err)
	}
	if rule["action"] != "allow" {
		t.Errorf("rule.action=%v; want allow", rule["action"])
	}
	if rule["target"] != "fs.read" {
		t.Errorf("rule.target=%v; want fs.read", rule["target"])
	}
	if rule["id"] != "allow-fs.read" {
		t.Errorf("rule.id=%v; want allow-fs.read", rule["id"])
	}

	var evidence map[string]any
	if err := json.Unmarshal(p.Evidence, &evidence); err != nil {
		t.Fatalf("Evidence not valid JSON: %v", err)
	}
	if int(evidence["denial_count"].(float64)) != 7 {
		t.Errorf("evidence.denial_count=%v; want 7", evidence["denial_count"])
	}
}

func TestClassifier_NoAPIKey_returnsUnclassifiedFallback(t *testing.T) {
	// When no Anthropic key is configured the classifier MUST NOT
	// fail — it returns a sentinel "unclassified" result so the
	// upstream Proposer can skip without proposing. Verifying this
	// pins the no-leak-on-empty-config contract.
	c := NewClassifier("", slog.Default())
	res, err := c.ClassifyDenial(context.Background(), "fs.write", `{"path":"/x"}`, 10)
	if err != nil {
		t.Fatalf("classify with empty key returned error: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	if res.IsGap {
		t.Error("IsGap=true on empty-key fallback; proposer would generate a noisy proposal")
	}
	if res.Confidence != 0 {
		t.Errorf("Confidence=%v; want 0 for unclassified fallback", res.Confidence)
	}
	if res.Category != "unclassified" {
		t.Errorf("Category=%q; want unclassified", res.Category)
	}
}
