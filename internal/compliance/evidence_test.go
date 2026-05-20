package compliance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/therelicai/therelic-platform/internal/storage"
)

type stubSource struct {
	audit []storage.AuditEvent
	runs  []storage.Run
}

func (s *stubSource) ListAuditEventsInWindow(_ context.Context, _ string, _, _ time.Time) ([]storage.AuditEvent, error) {
	return s.audit, nil
}
func (s *stubSource) ListRunsInWindow(_ context.Context, _ string, _, _ time.Time) ([]storage.Run, error) {
	return s.runs, nil
}

func writeTempMapping(t *testing.T, dir string) {
	t.Helper()
	yaml := `framework: Test Framework
version: "1.0"
controls:
  - id: T1
    name: First test control
    relic_satisfies: []
    coverage: complete
`
	if err := os.WriteFile(filepath.Join(dir, "test-fw.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBuildPack_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	writeTempMapping(t, tmp)

	signer, err := NewHMACSigner(strings.Repeat("ab", 16))
	if err != nil {
		t.Fatal(err)
	}
	src := &stubSource{
		audit: []storage.AuditEvent{
			{ID: "a1", Action: "trace.upload", Resource: "run", ResourceID: "r1"},
			{ID: "a2", Action: "agent.policy_update", Resource: "agent", ResourceID: "ag1"},
			{ID: "a3", Action: "identity.invite.create", Resource: "invite", ResourceID: "i1"},
		},
		runs: []storage.Run{
			{ID: "r1", OrgID: "o1", AgentName: "alpha", IntegrityChain: true, ChainVerified: true},
		},
	}

	var buf bytes.Buffer
	manifest, err := BuildPack(context.Background(), PackRequest{
		OrgID:       "o1",
		Framework:   "test-fw",
		Period:      "2026-Q1",
		Start:       time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		End:         time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
		Generator:   "relic-api test",
		MappingsDir: tmp,
		Signer:      signer,
	}, src, &buf)
	if err != nil {
		t.Fatalf("BuildPack: %v", err)
	}
	if manifest.Counts.AuditEvents != 3 {
		t.Errorf("AuditEvents=%d want 3", manifest.Counts.AuditEvents)
	}
	if manifest.Counts.PolicyChanges != 1 {
		t.Errorf("PolicyChanges=%d want 1", manifest.Counts.PolicyChanges)
	}
	if manifest.Counts.RBACChanges != 1 {
		t.Errorf("RBACChanges=%d want 1", manifest.Counts.RBACChanges)
	}
	if manifest.Counts.Runs != 1 {
		t.Errorf("Runs=%d want 1", manifest.Counts.Runs)
	}

	// Verify the pack round-trips against the same signer.
	m2, err := VerifyPack(bytes.NewReader(buf.Bytes()), signer)
	if err != nil {
		t.Fatalf("VerifyPack: %v", err)
	}
	if m2.Framework != "test-fw" || m2.OrgID != "o1" {
		t.Errorf("verified manifest wrong: %+v", m2)
	}
}

func TestVerifyPack_RejectsTamperedManifest(t *testing.T) {
	tmp := t.TempDir()
	writeTempMapping(t, tmp)
	signer, _ := NewHMACSigner(strings.Repeat("ab", 16))

	var orig bytes.Buffer
	if _, err := BuildPack(context.Background(), PackRequest{
		OrgID:       "o1",
		Framework:   "test-fw",
		Period:      "Q1",
		Start:       time.Now().Add(-24 * time.Hour),
		End:         time.Now(),
		Generator:   "test",
		MappingsDir: tmp,
		Signer:      signer,
	}, &stubSource{}, &orig); err != nil {
		t.Fatal(err)
	}

	// Tamper: extract, mutate manifest.json, re-tar without re-signing.
	tampered := tamperManifest(t, orig.Bytes())
	if _, err := VerifyPack(bytes.NewReader(tampered), signer); err == nil {
		t.Fatal("expected verify failure on tampered pack")
	}
}

func TestVerifyPack_RejectsWrongKey(t *testing.T) {
	tmp := t.TempDir()
	writeTempMapping(t, tmp)
	signer, _ := NewHMACSigner(strings.Repeat("ab", 16))
	wrong, _ := NewHMACSigner(strings.Repeat("cd", 16))

	var buf bytes.Buffer
	if _, err := BuildPack(context.Background(), PackRequest{
		OrgID:       "o1",
		Framework:   "test-fw",
		Period:      "Q1",
		Start:       time.Now().Add(-24 * time.Hour),
		End:         time.Now(),
		Generator:   "test",
		MappingsDir: tmp,
		Signer:      signer,
	}, &stubSource{}, &buf); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyPack(bytes.NewReader(buf.Bytes()), wrong); err == nil {
		t.Fatal("expected verify failure with wrong key")
	}
}

func TestBuildPack_RequiresEndAfterStart(t *testing.T) {
	tmp := t.TempDir()
	writeTempMapping(t, tmp)
	signer, _ := NewHMACSigner(strings.Repeat("ab", 16))
	now := time.Now()
	_, err := BuildPack(context.Background(), PackRequest{
		OrgID: "o1", Framework: "test-fw", Period: "x",
		Start: now, End: now, MappingsDir: tmp, Signer: signer,
	}, &stubSource{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "after Start") {
		t.Errorf("expected end-after-start error, got: %v", err)
	}
}

// tamperManifest unpacks the tarball, modifies manifest.json bytes,
// and re-packs (leaving the original signature in place).
func tamperManifest(t *testing.T, packed []byte) []byte {
	t.Helper()
	gr, _ := gzip.NewReader(bytes.NewReader(packed))
	tr := tar.NewReader(gr)
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	tw := tar.NewWriter(gw)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		body, _ := io.ReadAll(tr)
		if hdr.Name == "manifest.json" {
			var m Manifest
			_ = json.Unmarshal(body, &m)
			m.OrgID = "TAMPERED"
			body, _ = json.MarshalIndent(m, "", "  ")
			hdr.Size = int64(len(body))
		}
		_ = tw.WriteHeader(hdr)
		_, _ = tw.Write(body)
	}
	tw.Close()
	gw.Close()
	return out.Bytes()
}
