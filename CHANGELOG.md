# Changelog

All notable changes to therelic-platform are documented here. The format
is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
this project adheres to [Semantic Versioning](https://semver.org/) once
it cuts its first tag.

Cross-repo contracts referenced from entries below live in
[RELIC.md](./RELIC.md); endpoint shapes live in [docs/api.md](./docs/api.md).

## [Unreleased]

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
