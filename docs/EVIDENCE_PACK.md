# Evidence pack

Signed tarball you hand an auditor as proof that the controls listed
in the framework mapping (`internal/compliance/mappings/*.yaml`) are
in force for a specific compliance period.

## How to generate one

```bash
relic-api evidence-pack \
  --org=<org-id> \
  --framework=soc2-cc \
  --period=Q3-2026 \
  --start=2026-07-01 \
  --end=2026-10-01 \
  --sign-with=hmac \
  --out=q3-2026.tar.gz
```

`--sign-with` defaults to `hmac` (v0). `gpg` lands in v1 once we ship
per-org GPG keyring management.

Required env:
- `DATABASE_URL` — same database the API is using.
- `RELIC_EVIDENCE_KEY` (or `RELIC_JWT_SECRET` as fallback) — HMAC
  key. Must be 16+ bytes after hex-decode. Operators who rotate this
  key MUST keep the old key handy for verifying previously-issued
  packs; verification requires the key the pack was sealed with.

## What's in the pack

| File | Purpose |
|---|---|
| `manifest.json` | Org, framework, period, counts. Signed. |
| `manifest.json.sig` | Detached signature over the manifest bytes. |
| `controls.yaml` | Snapshot of the framework mapping at generation time. |
| `audit-log.ndjson` | All audit events within the period. |
| `policy-history.ndjson` | Subset: policy mutations only. |
| `rbac-changes.ndjson` | Subset: identity/api-key/org changes. |
| `runs.ndjson` | Agent runs within the period (storage keys stripped). |
| `chain-verification.json` | Per-run HMAC chain status. |
| `README.txt` | Auditor instructions, also embedded in the pack. |

## How to verify

```bash
relic-api evidence-verify q3-2026.tar.gz
# → VERIFIED · framework=soc2-cc · period=Q3-2026 · 47 audit events · 8 runs · sig=hmac key=ab12cd34
```

Or with the reference Python verifier (separate repo, MIT):

```bash
pip install therelic-evidence-verifier
relic-evidence-verify q3-2026.tar.gz --key=$RELIC_EVIDENCE_KEY
```

## Format version

The manifest's `format_version` field lets us evolve the pack schema
without breaking older verifiers. v0 is HMAC-only; v1 adds GPG
signatures + a `format_version=v1` manifest. The Python verifier
supports both transparently.

## Auditor's checklist

When you receive a pack:

1. Run `relic-evidence-verify` to confirm the signature.
2. Open `controls.yaml` and scan the `coverage` column. `gap` rows
   are explicit gaps to ask about.
3. Cross-reference `audit-log.ndjson` against the periods + users
   listed in your evidence request.
4. Spot-check 2-3 control IDs from the mapping — each has an
   `evidence` block with paths into the platform repo (public on
   GitHub) that you can review independently.

## What the pack does NOT contain

- Trace bodies (only metadata + chain status). Auditors request the
  trace contents separately through the customer's normal credentials.
- Customer PII outside what's in `audit-log.ndjson` (user_id,
  resource_id).
- The HMAC key. The verifier needs the same key out of band — same
  guarantee as a JWT.

## Roadmap

- **v1** — per-org GPG signing, format_version bump, Python verifier
  released as `therelic-evidence-verifier` on PyPI.
- **v2** — incremental packs (delta from last pack), so a 12-month
  audit window doesn't require re-rolling the entire database.
- **v3** — cryptographic linkage between packs (Merkle tree over
  prior pack signatures), so an auditor can detect a missing period.
