# Changelog

All notable changes to therelic-platform are documented here. The format
is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project adheres to [Semantic Versioning](https://semver.org/) once
it cuts its first tag.

Cross-repo contracts referenced from entries below live in
[RELIC.md](./RELIC.md); endpoint shapes live in [docs/api.md](./docs/api.md).

## [Unreleased]

### Added — Enterprise build (WS-1A, WS-1E, WS-2A-E, WS-3A-B, WS-4A, WS-5)

- **WS-1A · OIDC adapter** — `internal/auth/oidc.go` implements the
  full PKCE authorization-code flow against any OIDC IdP
  (Google/Okta/Entra/Auth0). Verifies ID-token signature, audience,
  issuer, nonce. HTTP surface: `GET /v1/auth/oidc/login`,
  `GET /v1/auth/oidc/callback`, `POST /v1/auth/oidc/logout`. Issues
  HS256 session tokens so the existing middleware path stays unchanged.
  `cmd/relic-api/main.go` wires `RELIC_AUTH_MODE=oidc`.
- **WS-1E · Identity surface** — migration `016_identity_config.sql`
  adds `sso_configs`, `scim_tokens`, `identity_invites`, and
  `sessions` tables. `internal/storage/identity.go` provides typed
  helpers; `internal/api/identity_handlers.go` mounts the
  `/v1/orgs/:id/identity/*` surface (SSO read/write, SCIM mint+revoke,
  invites, session list+revoke). Client-secret column is HMAC-
  enveloped at rest with `RELIC_SECRETS_KEY`.
- **WS-2D · Security headers + CSRF** — new middlewares in
  `internal/api/middleware/security_headers.go` and `csrf.go`. HSTS,
  X-Content-Type-Options, X-Frame-Options, Referrer-Policy,
  Permissions-Policy, COOP, CORP. CSRF via double-submit cookie;
  exempts API-key clients and sessionless requests so the CLI keeps
  working.
- **WS-2B · Backup completeness** — `relic-api backup --include-blobs`
  copies every S3 object into the tarball; `relic-api restore` pushes
  blobs back when present. `internal/storage/s3.go` adds `StreamObject`.
- **WS-2E · Signed-URL S3 access** — `GET /v1/traces/:id/events`
  redirects to a 5-min pre-signed R2 URL by default. `?inline=1`
  keeps the legacy stream-through-Go path for the OSS CLI.
  `internal/storage/s3.go` adds `PresignGet`.
- **WS-2A · Load test scaffolding** — `test/load/*.js` k6 scenarios
  (ingest sustained, burst, gateway concurrency, auth login rate
  limit, mixed realistic). `setup.sh` / `teardown.sh` provision a
  dedicated load-test Fly app. `docs/PERFORMANCE.md` + history file.
- **WS-2C · HA + observability** — `internal/storage/replica.go`
  adds optional `DATABASE_REPLICA_URL` routing via `db.Readonly()`.
  `internal/observability/otel.go` boots OTLP/HTTP exporters when
  `RELIC_OTEL_ENABLED=true`; no-op otherwise. New docs: `docs/HA.md`,
  `docs/SLO.md`.
- **WS-3A · Control mapping engine** — `internal/compliance/loader.go`
  loads YAML mappings under `internal/compliance/mappings/`
  (SOC 2 CC, ISO 27001, HIPAA §164). Every evidence path is validated
  against the working tree at load time so CI catches a broken
  reference. `docs/COMPLIANCE_MAPPINGS.md`.
- **WS-3B · Evidence pack export** — `internal/compliance/evidence.go`
  assembles a signed tarball (manifest, controls snapshot, audit log,
  policy history, RBAC changes, run records, chain verification).
  `internal/compliance/sign.go` provides HMAC-SHA256 signing
  (`Signer` interface; GPG slot reserved for v1). CLI:
  `relic-api evidence-pack` + `evidence-verify`. New storage helpers:
  `ListAuditEventsInWindow`, `ListRunsInWindow`. `docs/EVIDENCE_PACK.md`.
- **WS-4A · OTEL exporter** — `internal/integrations/otel/exporter.go`
  exposes `EmitPolicyDecision`, `EmitTraceIngest`, `EmitAuthLogin`;
  wired into trace upload + auth handlers. `docs/OTEL.md` documents
  per-backend snippets for Splunk, Datadog, Honeycomb, Elastic,
  New Relic, Grafana Cloud.
- **WS-5 · Hosting** — `fly.production.toml` (prod-only config),
  `ops/runbook.md`, `ops/deploy.sh`, `ops/migrate.sh`,
  `ops/rollback.sh`, `.github/workflows/deploy-api.yml`. Live deploy
  itself requires Fly/Cloudflare/Neon/R2 credentials and is not
  exercised here.

### Added — Slice 15: Universal policy enforcement

- **New migration `012_policy_sets_labels.sql`** — `policy_sets` table
  (id, org_id, name, selector JSONB, policy_yaml, policy_hash, version),
  `agent_labels` table (agent_id, key, value), and `applied_policy_hash`
  / `applied_at` columns on `agents`.
- **Storage helpers** in `internal/storage/postgres.go`: `UpsertPolicySet`
  (version-bumping upsert), `GetPolicySetByID`, `GetPolicySetByName`,
  `SetAgentLabels` (transactional overwrite), `GetAgentLabels`,
  `ResolveSelector` (handles `{ agent_name }` and `{ match: {…} }`
  arms; the label-match arm AND's across keys via a single
  `GROUP BY a.id HAVING COUNT(DISTINCT al.key) = N` query),
  `MarkPolicyApplied`.
- **New endpoints**:
  - `POST /v1/policy_sets` + `PUT /v1/policy_sets/:id` — upsert by
    (org, name). Parses + validates YAML synchronously, persists the
    row, fans out a `policyfeed.Notification` per matched agent.
  - `GET /v1/policy_sets/:id` — set + currently-resolved matched
    agents + per-agent applied state (the dashboard's "47/52 on
    hash abc123" payload).
  - `POST /v1/policy_sets/resolve` — read-only selector preview the
    editor calls on every selector change.
  - `POST /v1/agents/:name/labels` — overwrites the agent's label set.
  - `POST /v1/agents/:name/policy_applied` — runtime closes the apply
    loop here.
  - `GET /v1/agents/:name/policy_updates` (SSE, agent-facing) — fans
    out policy update notifications. Distinct from `/v1/orgs/:id/live`
    in audience, auth, and event shape.
- **New package `internal/policyfeed/`** — Postgres LISTEN/NOTIFY hub
  on channel `relic_policy_updates`, per-(org, agent) subscriber
  bounded channels with drop-on-overflow. Mirrors the `livefeed`
  pattern; sibling package because the shape and consumer are
  different.
- **`storage.Postgres.Pool()`** now used by both `livefeed` and
  `policyfeed`. Hub Start runs before HTTP accepts traffic so a
  listener failure surfaces at boot.

### Constraints respected (slice 15)

- No new infrastructure dependencies (LISTEN/NOTIFY in Postgres).
- `ActionEvent`, `RunEvent`, `PolicyReloadEvent` untouched.
- The dashboard-facing SSE channel from slice 14 is unchanged; the
  agent-facing channel is a *separate* endpoint with separate auth.
- The simulator endpoint shape is unchanged. The dashboard's diff
  badge picks the first resolved agent as a representative when the
  selector matches multiple agents — extending the simulator to
  fan out internally is reserved for a follow-up slice if needed.

### Tests added

- `internal/policyfeed/hub_test.go` — agent-scoped dispatch, org
  isolation across same-named agents, slow-consumer drop,
  subscriber count tracking.
- `internal/api/policy_sets_test.go` — wire-level auth check on all
  seven slice-15 endpoints.

### Added — Slice 14: Live fleet observability

#### Slice 14a — `last_seen` correctness fix

- **`storage.UpdateAgentLastSeen(ctx, orgID, agentName)`** — single
  `UPDATE` helper.
- **`handleUploadTrace`** now calls it on every successful trace
  upload so the dashboard's "Online" pill is accurate for agents that
  registered once and ran without re-registering. Failure is logged,
  not fatal — the upload's success is independent.

#### Slice 14b — Streaming intents and dashboard SSE

- **`POST /v1/intents`** — runtimes push a single sealed `intent` or
  `action` event per request. Body cap 32 KiB; rejects unsupported
  `t` values with 400. Authenticated with an org-scoped API key.
- **`GET /v1/orgs/:orgID/live`** — dashboard-facing Server-Sent
  Events stream. Authenticated by user JWT; path `orgID` must match
  the JWT's resolved org. Query params `agent_name`, `tool`, `verdict`
  filter server-side. Comment-frame keep-alive every 25s.
- **`internal/livefeed/`** — Postgres LISTEN/NOTIFY pub/sub hub.
  Single channel `relic_live`; NOTIFY payload carries `org_id` so
  subscribers filter by tenant from auth context. Per-subscriber
  bounded channel (128 events), drop-on-overflow.
- **`storage.Postgres.Pool()`** — exported accessor for the
  livefeed's dedicated LISTEN connection. Pool ownership stays with
  `*storage.Postgres`.
- **`UpdateAgentLastSeen` layered into `/v1/intents`** so streaming
  runtimes keep the Online pill fresh without waiting for batch
  upload.

### Constraints respected (slice 14)

- ActionEvent / RunEvent / PolicyReloadEvent untouched.
- No new infrastructure dependencies — LISTEN/NOTIFY is built into
  Postgres. The 8 KiB NOTIFY payload limit is the binding constraint;
  oversized envelopes are rejected at publish time.
- Existing batch trace ingest path (`POST /v1/traces`) is unchanged.
  Streaming is strictly additive; batch push remains the durable
  fallback.
- Cross-tenant isolation enforced at dispatch time inside the hub —
  events for org A can never reach a subscriber on org B even with
  matching filters.

### Tests added

- `internal/livefeed/hub_test.go` — filter matching, org scoping,
  filter-within-org, slow-consumer drop, subscriber count.
- `internal/api/live_test.go` — wire-level auth check on the new
  routes.

### Added — Slice 13: Replay & diff badge

- **Policy simulator**: new endpoints `POST /v1/policy/simulate` and
  `GET /v1/policy/simulate/:job_id`. Submit a candidate policy + selector
  + window; the platform replays the candidate against the org's
  recorded action stream and reports how verdicts would have changed,
  with up to 5 sample run IDs per direction (newly_denied / newly_allowed).
- **Vendored policy engine** at [internal/policy/](./internal/policy/) —
  byte-for-byte mirror of the runtime's `internal/policy/` (engine,
  parser, sequence files). Pinned upstream SHA recorded in
  [UPSTREAM.txt](./internal/policy/UPSTREAM.txt). A new CI job
  `policy-drift` refuses any drift so the simulator's verdicts cannot
  diverge from the runtime's enforcement.
- **`policy.Simulate`** (platform-only) — pure function over a candidate
  `*Policy` + recorded `[]ActionEvent`. Calls the canonical `Evaluate`,
  buckets results into `newly_denied / newly_allowed / unchanged`.
- **`trace.ExtractActionEvents`** — defensive NDJSON extractor that yields
  `policy.ActionEvent` per `t:"action"` line. Reuses the existing parser's
  size/event caps.
- **`storage.ListRunsForSimulate`** — time-windowed run lookup keyed by
  `(org_id, agent_name, started_at >= since)`, capped at 200 runs per
  simulation.
- **`simulate.Runner`** — in-memory job orchestrator with bounded
  concurrency (4 traces in flight) and per-job timeout (60s).
- **Audit event** `policy.simulate` records every submit.
- **`RELIC.md`** created — cross-repo alignment document defining selector
  contract, event shapes, and replay protocol.
- **`docs/api.md`** created — full endpoint reference, replacing
  endpoint-detail duplication in the README and architecture docs.

### Changed

- README's "API Endpoints" table summarizes only; full schemas now live in
  [docs/api.md](./docs/api.md). Documentation index links to RELIC.md and
  CHANGELOG.md.
- `go.mod` adds `github.com/bmatcuk/doublestar/v4` and `gopkg.in/yaml.v3`
  (required by the vendored policy package).

### Internal contracts (not API-breaking)

- `Server` now carries an optional `*simulate.Runner`. Constructor signature
  unchanged; the runner is attached via `WithSimulator(...)` from the
  binary entrypoint.
