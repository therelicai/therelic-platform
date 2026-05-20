package compliance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot returns the platform repo root relative to this test file.
// We're at internal/compliance, so up two levels.
func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func TestLoadAll_RealMappings(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal/compliance/mappings")
	all, err := LoadAll(dir, root)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	wantSlugs := []string{"soc2-cc", "iso-27001", "hipaa-164"}
	for _, slug := range wantSlugs {
		m, ok := all[slug]
		if !ok {
			t.Errorf("missing mapping: %s", slug)
			continue
		}
		if len(m.Controls) == 0 {
			t.Errorf("%s has no controls", slug)
		}
	}
}

func TestLoadAll_FrameworkAndVersionPresent(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "internal/compliance/mappings")
	all, err := LoadAll(dir, root)
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	for slug, m := range all {
		if strings.TrimSpace(m.Framework) == "" {
			t.Errorf("%s: framework missing", slug)
		}
		if strings.TrimSpace(m.Version) == "" {
			t.Errorf("%s: version missing", slug)
		}
	}
}

func TestLoad_RejectsBadEvidencePath(t *testing.T) {
	tmp := t.TempDir()
	badYAML := `framework: Test
version: "1.0"
controls:
  - id: T1
    name: Test control
    relic_satisfies:
      - capability: x
        evidence:
          - kind: code
            path: definitely/does/not/exist.go
    coverage: complete
`
	mappingPath := filepath.Join(tmp, "test.yaml")
	if err := os.WriteFile(mappingPath, []byte(badYAML), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(mappingPath, tmp); err == nil {
		t.Fatal("expected error for broken path, got nil")
	} else if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected 'does not exist' error, got: %v", err)
	}
}

func TestLoad_RejectsDuplicateControlIDs(t *testing.T) {
	tmp := t.TempDir()
	dupYAML := `framework: Test
version: "1.0"
controls:
  - id: SAME
    name: First
    relic_satisfies: []
    coverage: complete
  - id: SAME
    name: Duplicate
    relic_satisfies: []
    coverage: complete
`
	mappingPath := filepath.Join(tmp, "dup.yaml")
	if err := os.WriteFile(mappingPath, []byte(dupYAML), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(mappingPath, ""); err == nil {
		t.Fatal("expected duplicate-id error, got nil")
	} else if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected 'duplicate' error, got: %v", err)
	}
}

func TestLoad_RejectsInvalidCoverage(t *testing.T) {
	tmp := t.TempDir()
	badYAML := `framework: Test
version: "1.0"
controls:
  - id: X1
    name: x
    relic_satisfies: []
    coverage: maybe
`
	mappingPath := filepath.Join(tmp, "bad-cov.yaml")
	if err := os.WriteFile(mappingPath, []byte(badYAML), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(mappingPath, ""); err == nil {
		t.Fatal("expected coverage error, got nil")
	} else if !strings.Contains(err.Error(), "coverage") {
		t.Errorf("expected 'coverage' error, got: %v", err)
	}
}

func TestLoad_DocsAnchorAllowed(t *testing.T) {
	root := repoRoot(t)
	tmp := t.TempDir()
	// Stage a real docs file in the temp dir as the repo root for
	// existence resolution.
	if err := os.WriteFile(filepath.Join(tmp, "real.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	yaml := `framework: Test
version: "1.0"
controls:
  - id: A
    name: Test
    relic_satisfies:
      - capability: x
        evidence:
          - kind: docs
            path: real.md#section-1
    coverage: complete
`
	mappingPath := filepath.Join(tmp, "anchor.yaml")
	if err := os.WriteFile(mappingPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(mappingPath, tmp); err != nil {
		t.Fatalf("Load: %v", err)
	}
	_ = root // unused; kept to mirror the other tests' shape
}
