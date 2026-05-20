# Compliance mappings

Authoritative map of compliance-framework controls to Relic
capabilities and the code/docs that substantiate them. Used by the
evidence-pack export (WS-3B) and by auditors who want to trust-but-
verify our coverage claims.

## Where the files live

`internal/compliance/mappings/*.yaml` — one file per framework. The
file slug (no `.yaml`) is the key the evidence-pack `--framework`
flag accepts.

| Slug | Framework | Version |
|---|---|---|
| `soc2-cc` | SOC 2 Common Criteria | 2017 with 2022 Points of Focus |
| `iso-27001` | ISO/IEC 27001 Annex A | 2022 |
| `hipaa-164` | HIPAA Security Rule | 45 CFR §164 |

## Schema

```yaml
framework: <human-readable name>
version: <version string>
controls:
  - id: <control id, unique within the file>
    name: <control name>
    relic_satisfies:
      - capability: <slug — short, lowercase>
        evidence:
          - kind: code | docs | metric | process
            path: <path relative to repo root; docs may include #anchor>
        notes: |
          Optional free text explaining how the capability satisfies
          the control. Shown to auditors verbatim in the evidence
          pack.
    coverage: complete | partial | gap | not_applicable
```

### Coverage states

- `complete` — the listed evidence fully substantiates the control.
- `partial` — Relic addresses part of the control; the remainder is
  the customer's responsibility (e.g. physical access controls).
- `gap` — open gap. We're explicit about gaps so an auditor doesn't
  read "complete" everywhere and assume we're hiding something.
- `not_applicable` — the control is out of scope for a managed
  software product (e.g. workforce clearance procedures).

## How to add or update a mapping

1. Edit the YAML file under `internal/compliance/mappings/`.
2. Run `go test ./internal/compliance/...` — every evidence path must
   resolve to a real file under the repo root, or the test fails.
3. Include the change in the PR description so auditors auto-watching
   `CHANGELOG.md` notice.

## How to add a new framework

1. Drop a new YAML file at `internal/compliance/mappings/<slug>.yaml`.
2. Follow the schema above.
3. Add the row to the table at the top of this doc.
4. Bump the test suite — `TestLoadAll_RealMappings` checks for the
   expected slug list.

## Anti-goals

- Mappings are not a checklist for engineers to "pass an audit". They
  exist to help an auditor verify our claims. If a control is a gap,
  say "gap"; the open list is more credible than fake coverage.
- Mappings are not an excuse to add a control without writing the
  code. Don't pad with `not_applicable` or `partial` to inflate
  coverage rates. The compliance-mapping engine is *not* a roadmap;
  the roadmap is.
