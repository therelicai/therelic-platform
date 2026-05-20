// Package compliance loads + validates framework-to-capability
// mappings used by the evidence-pack export (WS-3B).
//
// Mappings live in internal/compliance/mappings/*.yaml; one file per
// framework. The loader walks the YAML, asserts every referenced
// evidence path exists in the working tree, and rejects duplicates.
// Run via go test ./internal/compliance/... — CI will catch a bad
// path before it reaches a customer pack.
package compliance

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Coverage classifies how well Relic claims to satisfy a control.
// Strings, not an enum, so YAML reads cleanly.
type Coverage string

const (
	CoverageComplete      Coverage = "complete"
	CoveragePartial       Coverage = "partial"
	CoverageGap           Coverage = "gap"
	CoverageNotApplicable Coverage = "not_applicable"
)

func (c Coverage) Valid() bool {
	switch c {
	case CoverageComplete, CoveragePartial, CoverageGap, CoverageNotApplicable:
		return true
	}
	return false
}

// EvidenceKind is the shape of one piece of evidence: usually a code
// path or a docs path. Adding new kinds (e.g. "metric") requires both
// the YAML schema and the evidence-pack assembly code to learn them.
type EvidenceKind string

const (
	EvidenceCode    EvidenceKind = "code"
	EvidenceDocs    EvidenceKind = "docs"
	EvidenceMetric  EvidenceKind = "metric"
	EvidenceProcess EvidenceKind = "process"
)

func (k EvidenceKind) Valid() bool {
	switch k {
	case EvidenceCode, EvidenceDocs, EvidenceMetric, EvidenceProcess:
		return true
	}
	return false
}

// Evidence is one row of the audit-substantiation table.
type Evidence struct {
	Kind EvidenceKind `yaml:"kind"`
	Path string       `yaml:"path"`
}

// Satisfaction is one capability the platform claims toward a control.
type Satisfaction struct {
	Capability string     `yaml:"capability"`
	Evidence   []Evidence `yaml:"evidence"`
	Notes      string     `yaml:"notes,omitempty"`
}

// Control is one row of the framework's control catalog.
type Control struct {
	ID             string         `yaml:"id"`
	Name           string         `yaml:"name"`
	RelicSatisfies []Satisfaction `yaml:"relic_satisfies"`
	Coverage       Coverage       `yaml:"coverage"`
}

// Mapping is the top-level YAML document — one framework per file.
type Mapping struct {
	Framework string    `yaml:"framework"`
	Version   string    `yaml:"version"`
	Controls  []Control `yaml:"controls"`
}

// Load parses a mapping YAML and validates it. Returns the parsed
// document on success. Validation rules:
//   - framework + version present
//   - controls non-empty
//   - control ids unique within the file
//   - coverage one of the named values
//   - every evidence.path resolves to a real file (resolved relative
//     to repoRoot)
//   - every evidence.kind is one of the named values
//
// repoRoot is the directory paths get joined against — the platform's
// repo root in normal use, an empty string skips path-existence
// checks (e.g. when loading a mapping from a customer-uploaded pack).
func Load(path, repoRoot string) (*Mapping, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	body, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var m Mapping
	if err := yaml.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := validate(&m, repoRoot); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return &m, nil
}

// LoadAll loads every *.yaml mapping in dir. Returns map keyed by
// framework slug ("soc2-cc", "iso-27001"). Useful for the evidence
// pack export which lets the operator pick a framework by slug.
func LoadAll(dir, repoRoot string) (map[string]*Mapping, error) {
	out := map[string]*Mapping{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		m, err := Load(path, repoRoot)
		if err != nil {
			return nil, err
		}
		slug := strings.TrimSuffix(e.Name(), ".yaml")
		out[slug] = m
	}
	return out, nil
}

func validate(m *Mapping, repoRoot string) error {
	if strings.TrimSpace(m.Framework) == "" {
		return errors.New("framework is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return errors.New("version is required")
	}
	if len(m.Controls) == 0 {
		return errors.New("controls must be non-empty")
	}
	seen := map[string]bool{}
	for i, c := range m.Controls {
		if strings.TrimSpace(c.ID) == "" {
			return fmt.Errorf("control[%d]: id is required", i)
		}
		if seen[c.ID] {
			return fmt.Errorf("control[%d]: duplicate id %q", i, c.ID)
		}
		seen[c.ID] = true
		if !c.Coverage.Valid() {
			return fmt.Errorf("control %s: invalid coverage %q (complete|partial|gap|not_applicable)", c.ID, c.Coverage)
		}
		for si, sat := range c.RelicSatisfies {
			if strings.TrimSpace(sat.Capability) == "" {
				return fmt.Errorf("control %s, satisfaction[%d]: capability is required", c.ID, si)
			}
			for ei, ev := range sat.Evidence {
				if !ev.Kind.Valid() {
					return fmt.Errorf("control %s, evidence[%d]: invalid kind %q", c.ID, ei, ev.Kind)
				}
				if strings.TrimSpace(ev.Path) == "" {
					return fmt.Errorf("control %s, evidence[%d]: path is required", c.ID, ei)
				}
				if repoRoot == "" {
					continue
				}
				// docs paths can use a #section anchor; strip it
				// for existence checking.
				p := ev.Path
				if hash := strings.Index(p, "#"); hash >= 0 {
					p = p[:hash]
				}
				full := filepath.Join(repoRoot, p)
				if _, err := os.Stat(full); err != nil {
					return fmt.Errorf("control %s, evidence path %q does not exist (looked at %s)", c.ID, ev.Path, full)
				}
			}
		}
	}
	return nil
}
