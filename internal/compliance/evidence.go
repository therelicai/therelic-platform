package compliance

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/therelicai/therelic-platform/internal/storage"
)

// PackRequest is the parameters of an evidence-pack export.
type PackRequest struct {
	OrgID         string
	Framework     string // slug matching internal/compliance/mappings/<slug>.yaml
	Period        string // free-form label like "Q3-2026"
	Start, End    time.Time
	Generator     string // "relic-api <version>"
	MappingsDir   string // path to mappings (defaults handled by caller)
	Signer        Signer
}

// Manifest is the top-of-pack JSON. Mirrors the structure called out
// in the build plan; format_version lets future schema changes ride
// without breaking older verifiers.
type Manifest struct {
	FormatVersion string    `json:"format_version"`
	OrgID         string    `json:"org_id"`
	Framework     string    `json:"framework"`
	Period        string    `json:"period"`
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
	GeneratedAt   time.Time `json:"generated_at"`
	Generator     string    `json:"generator"`
	Signature     struct {
		Kind  string `json:"kind"`
		KeyID string `json:"key_id"`
	} `json:"signature"`
	Counts struct {
		AuditEvents   int `json:"audit_events"`
		PolicyChanges int `json:"policy_changes"`
		Runs          int `json:"runs"`
		RBACChanges   int `json:"rbac_changes"`
	} `json:"counts"`
}

// Source is what evidence.go reads from. Defined as an interface so
// tests can stub out the DB.
type Source interface {
	ListAuditEventsInWindow(ctx context.Context, orgID string, start, end time.Time) ([]storage.AuditEvent, error)
	ListRunsInWindow(ctx context.Context, orgID string, start, end time.Time) ([]storage.Run, error)
}

// BuildPack writes the evidence pack to out. Returns the manifest
// (already serialized into the tarball). Callers typically just want
// the bytes-written + the manifest's signature for logging.
func BuildPack(ctx context.Context, req PackRequest, src Source, out io.Writer) (*Manifest, error) {
	if req.OrgID == "" {
		return nil, fmt.Errorf("OrgID required")
	}
	if req.Framework == "" {
		return nil, fmt.Errorf("Framework required")
	}
	if req.End.Before(req.Start) || req.End.Equal(req.Start) {
		return nil, fmt.Errorf("End must be after Start")
	}
	if req.Signer == nil {
		return nil, fmt.Errorf("Signer required")
	}

	mappingPath := filepath.Join(req.MappingsDir, req.Framework+".yaml")
	mappingBytes, err := os.ReadFile(mappingPath)
	if err != nil {
		return nil, fmt.Errorf("read mapping %s: %w", mappingPath, err)
	}

	auditEvents, err := src.ListAuditEventsInWindow(ctx, req.OrgID, req.Start, req.End)
	if err != nil {
		return nil, fmt.Errorf("audit window: %w", err)
	}
	runs, err := src.ListRunsInWindow(ctx, req.OrgID, req.Start, req.End)
	if err != nil {
		return nil, fmt.Errorf("runs window: %w", err)
	}

	policyChanges := filterPolicyChanges(auditEvents)
	rbacChanges := filterRBACChanges(auditEvents)

	manifest := Manifest{
		FormatVersion: "v0",
		OrgID:         req.OrgID,
		Framework:     req.Framework,
		Period:        req.Period,
		Start:         req.Start,
		End:           req.End,
		GeneratedAt:   time.Now().UTC(),
		Generator:     req.Generator,
	}
	manifest.Signature.Kind = req.Signer.Kind()
	manifest.Signature.KeyID = req.Signer.KeyID()
	manifest.Counts.AuditEvents = len(auditEvents)
	manifest.Counts.Runs = len(runs)
	manifest.Counts.PolicyChanges = len(policyChanges)
	manifest.Counts.RBACChanges = len(rbacChanges)

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	manifestSig, err := req.Signer.Sign(manifestJSON)
	if err != nil {
		return nil, err
	}

	// Build tar.gz on the wire.
	gz := gzip.NewWriter(out)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	if err := writeEntry(tw, "manifest.json", manifestJSON); err != nil {
		return nil, err
	}
	if err := writeEntry(tw, "manifest.json.sig", manifestSig); err != nil {
		return nil, err
	}
	if err := writeEntry(tw, "controls.yaml", mappingBytes); err != nil {
		return nil, err
	}
	if err := writeEntry(tw, "audit-log.ndjson", encodeNDJSON(auditEvents)); err != nil {
		return nil, err
	}
	if err := writeEntry(tw, "policy-history.ndjson", encodeNDJSON(policyChanges)); err != nil {
		return nil, err
	}
	if err := writeEntry(tw, "rbac-changes.ndjson", encodeNDJSON(rbacChanges)); err != nil {
		return nil, err
	}
	runsJSON, err := encodeRunsNDJSON(runs)
	if err != nil {
		return nil, err
	}
	if err := writeEntry(tw, "runs.ndjson", runsJSON); err != nil {
		return nil, err
	}
	// chain-verification.json: per-run integrity status. The pack
	// reader (auditor) checks this against the run records inside the
	// trace files; we include the summary up front for quick review.
	chainStatus := buildChainStatus(runs)
	chainJSON, _ := json.MarshalIndent(chainStatus, "", "  ")
	if err := writeEntry(tw, "chain-verification.json", chainJSON); err != nil {
		return nil, err
	}
	if err := writeEntry(tw, "README.txt", buildReadme(&manifest)); err != nil {
		return nil, err
	}

	return &manifest, nil
}

// VerifyPack reads a pack tarball, checks the manifest signature, and
// returns the manifest. Used by the `relic-api evidence-verify`
// subcommand.
func VerifyPack(in io.Reader, signer Signer) (*Manifest, error) {
	gr, err := gzip.NewReader(in)
	if err != nil {
		return nil, fmt.Errorf("gunzip: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)

	var manifestRaw, manifestSig []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch hdr.Name {
		case "manifest.json":
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			manifestRaw = b
		case "manifest.json.sig":
			b, err := io.ReadAll(tr)
			if err != nil {
				return nil, err
			}
			manifestSig = b
		}
	}
	if manifestRaw == nil || manifestSig == nil {
		return nil, fmt.Errorf("pack missing manifest or signature")
	}
	if err := signer.Verify(manifestRaw, manifestSig); err != nil {
		return nil, fmt.Errorf("signature verify: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(manifestRaw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	return &m, nil
}

// --- helpers ---

func writeEntry(tw *tar.Writer, name string, body []byte) error {
	hdr := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), ModTime: time.Now()}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(body)
	return err
}

func encodeNDJSON[T any](items []T) []byte {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	for _, it := range items {
		_ = enc.Encode(it)
	}
	return []byte(sb.String())
}

// encodeRunsNDJSON serializes runs, stripping the internal-only
// storage_key field per the JSON tag on storage.Run.
func encodeRunsNDJSON(runs []storage.Run) ([]byte, error) {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	for _, r := range runs {
		// Run already has `json:"-"` on StorageKey so the default
		// encoder will omit it.
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	return []byte(sb.String()), nil
}

// filterPolicyChanges returns audit events whose action is a policy
// mutation. The action constants live in internal/api/audit.go; we
// match against the string form to keep this package free of import
// cycles.
func filterPolicyChanges(events []storage.AuditEvent) []storage.AuditEvent {
	out := []storage.AuditEvent{}
	for _, e := range events {
		if e.Action == "agent.policy_update" ||
			e.Action == "policy_set.write" ||
			e.Action == "policy.simulate" {
			out = append(out, e)
		}
	}
	return out
}

func filterRBACChanges(events []storage.AuditEvent) []storage.AuditEvent {
	out := []storage.AuditEvent{}
	for _, e := range events {
		if strings.HasPrefix(e.Action, "identity.") || e.Action == "org.create" ||
			e.Action == "apikey.create" || e.Action == "apikey.revoke" {
			out = append(out, e)
		}
	}
	return out
}

type chainStatusEntry struct {
	RunID         string `json:"run_id"`
	AgentName     string `json:"agent_name"`
	HasChain      bool   `json:"has_chain"`
	ChainVerified bool   `json:"chain_verified"`
}

func buildChainStatus(runs []storage.Run) []chainStatusEntry {
	out := make([]chainStatusEntry, 0, len(runs))
	for _, r := range runs {
		out = append(out, chainStatusEntry{
			RunID: r.ID, AgentName: r.AgentName,
			HasChain: r.IntegrityChain, ChainVerified: r.ChainVerified,
		})
	}
	return out
}

func buildReadme(m *Manifest) []byte {
	return []byte(fmt.Sprintf(`The Relic Evidence Pack
=======================

Framework:    %s
Period:       %s
Start:        %s
End:          %s
Generated:    %s
Generator:    %s
Signature:    %s (key id: %s)

Format:       %s (see https://therelic.dev/docs/evidence-pack)

Files:
  manifest.json          — Top-of-pack summary + counts. Signed.
  manifest.json.sig      — Detached signature over manifest.json bytes.
  controls.yaml          — Snapshot of the framework-to-capability map
                           in force at generation time.
  audit-log.ndjson       — All audit events within the period.
  policy-history.ndjson  — Subset of audit events: policy mutations.
  rbac-changes.ndjson    — Subset of audit events: identity / api-key
                           / org changes.
  runs.ndjson            — Agent runs within the period (storage keys
                           omitted; trace bodies are accessible through
                           the API with the customer's credentials).
  chain-verification.json — Per-run HMAC chain status.

Verification:
  $ relic-api evidence-verify <this-file.tar.gz>

  Or with the reference verifier:
  $ pip install therelic-evidence-verifier
  $ relic-evidence-verify <this-file.tar.gz> --key=<hmac-key-hex>

Audit trail:
  Every line in audit-log.ndjson includes user_id, action, resource,
  resource_id, metadata, and created_at. The trace HMAC chain is
  documented separately in docs/THREAT_MODEL.md §4.
`,
		m.Framework, m.Period, m.Start.Format(time.RFC3339), m.End.Format(time.RFC3339),
		m.GeneratedAt.Format(time.RFC3339), m.Generator,
		m.Signature.Kind, m.Signature.KeyID, m.FormatVersion))
}
