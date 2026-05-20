# RELIC.md — cross-repo alignment document

> **Position (end goal):** The Relic is the single plane where every agent
> reports what it's about to do, where one policy change applies to all of
> them instantly, with confidence that nothing breaks because every rule is
> testable against your real history before you ship it.

This file is the **single source of truth** for cross-repo contracts that
bind [therelic](https://github.com/therelicai/therelic),
[therelic-platform](https://github.com/therelicai/therelic-platform),
[therelic-app](https://github.com/therelicai/therelic-app), and
[therelic-website](https://github.com/therelicai/therelic-website) together.
If a contract needs to change, **update this file first**, then propagate
to the four repos in the order listed above.

The other three repos link here from their READMEs and do not duplicate
its contents.

---

## Repo roles (non-negotiable)

| Repo | Role | License | Talks to |
|---|---|---|---|
| `therelic` | Open-source CLI + runtime (Go). Collector and enforcer. Installed on user machines/servers. | Apache 2.0 | `therelic-platform` (only when configured) |
| `therelic-platform` | Hosted API (Go, Postgres, S3). Multi-tenant brain. No UI. | Apache 2.0 | `therelic`, `therelic-app` |
| `therelic-app` | Web console (React/TypeScript). Owns presentation, no business logic. | Apache 2.0 | `therelic-platform` |
| `therelic-website` | Static marketing site. No product surface, no auth. | — | nothing |

The runtime works standalone (no `RELIC_API_KEY` set): traces stay local,
streaming is skipped, batch push is the durable on-reconnect path. This is
a first-class supported configuration.

---

## Currently shipping

| Capability | Ships in | Where it lives |
|---|---|---|
| Policy enforcement + trace audit | pre-slice-13 | `therelic` runtime |
| Hosted indexing + governance proposals | pre-slice-13 | `therelic-platform` |
| **Replay & diff badge** ("test every rule against your real history before you ship it") | **slice 13** | platform endpoints `POST /v1/policy/simulate` + `GET /v1/policy/simulate/:job_id`; app diff badge in the policy editor |
| **Live observability** ("every agent in your org, acting in real time") | **slice 14b** | runtime emits `IntentEvent` + streams via `POST /v1/intents`; platform fans out via Postgres LISTEN/NOTIFY; app reads from `GET /v1/orgs/:id/live` SSE |
| **`agents.last_seen` correctness** (Online pill reflects activity, not just registration) | **slice 14a** | `handleUploadTrace` + `handlePostIntent` both update `last_seen` |
| **Universal policy** ("one policy change applies to the labeled set within seconds") | **slice 15** | `policy_sets` + `agent_labels` tables; `POST /v1/policy_sets`, `POST /v1/agents/:name/labels`, agent-facing `GET /v1/agents/:name/policy_updates` (SSE), `POST /v1/agents/:name/policy_applied`; runtime subscribes on startup when `--watch` + API creds; in-flight Evaluate + HMAC chain key invariants preserved across hot reload |

---

## Selector contract

Every API contract that targets agents accepts a **selector**, not a raw
agent name — even when the selector resolves to a single agent today. This
is forward-compatible with slice 15's label-match grammar.

```json
// Single-agent form (slice 13).
{ "agent_selector": { "agent_name": "code-assist" } }

// Label-match form (slice 15). AND across keys; equality on values.
{ "agent_selector": { "match": { "env": "prod", "tier": "primary" } } }
```

Both arms remain valid; the single-agent form is not deprecated.
Empty selectors are rejected with `400 Bad Request`.

The platform's resolver (`storage.ResolveSelector`):

- `{ "agent_name": "X" }` → `WHERE agent_name = $1` (one row).
- `{ "match": {…} }` → joins `agent_labels` once per key with
  `GROUP BY a.id HAVING COUNT(DISTINCT al.key) = N`. AND-only for
  slice 15; future grammar additions (`not_match`, `any_of`) slot in
  without breaking the existing arms.

---

## Replay protocol (slice 13)

The simulator runs the same `Engine.Evaluate` code path the runtime uses
to enforce in production. The platform vendors the policy package from the
runtime ([therelic-platform/internal/policy/](./internal/policy/)); a CI
job ([.github/workflows/ci.yml](./.github/workflows/ci.yml), job
`policy-drift`) refuses any drift. The diff badge cannot lie about what
production would do because production runs the same code.

**Endpoints** (see [docs/api.md](./docs/api.md) for full schemas):

- `POST /v1/policy/simulate` — submit `{ policy_yaml, agent_selector, window_days }`. Window must be `1`, `7`, or `30`. Returns `{ job_id }` (202).
- `GET /v1/policy/simulate/:job_id` — poll. Returns `{ status, result? }` where status is `pending | running | done | error`.

**Result shape:**

```json
{
  "newly_denied": 4,
  "newly_allowed": 0,
  "unchanged": 120,
  "total_evaluated": 124,
  "runs_scanned": 18,
  "samples": [
    { "run_id": "01J9…", "target": "exec_shell", "method": "tool_call",
      "old_auth": "allow", "new_auth": "deny" }
  ]
}
```

Up to five samples per direction (newly_denied / newly_allowed) are
returned. Jobs are in-memory: if the API restarts mid-job, clients see
`status: error` and re-submit.

---

## Trace event contracts

The runtime's wire format is **additive, never breaking**. New event types
may be added freely. Existing event types (`run`, `action`, `policy_reload`)
and their fields **MUST NOT** be modified, renamed, removed, or
re-semanticized.

| Event type | Slice introduced | Status |
|---|---|---|
| `run` (status: start \| end) | pre-slice-13 | Frozen |
| `action` | pre-slice-13 | Frozen |
| `policy_reload` | pre-slice-13 | Frozen — slice 15 reuses (SSE-driven re-pull writes the same event type) |
| `intent` | slice 14 | Shipped (additive; pairs with `action` via shared `seq`) |

The HMAC chain in
[therelic-platform/internal/trace/integrity.go](./internal/trace/integrity.go)
is type-agnostic; new sealed event types extend the chain safely. The
parser counts only `t == "action"` for the run summary.

---

## Intent event contract (slice 14)

The runtime emits an `IntentEvent` between intent parsing and the
policy engine's verdict — and an `ActionEvent` after the verdict, as
today. Both events share the same `seq` so subscribers can pair "agent
wants to do X" with "X was {allowed|denied}".

```json
// IntentEvent — additive; ActionEvent unchanged.
{
  "v": 1,
  "t": "intent",
  "ts": "2026-05-01T12:00:00.000000123Z",
  "run": "01J…",
  "seq": 42,
  "proto": "mcp",
  "method": "tool_call",
  "target": "web_search",
  "params": { "...": "redacted before write" },
  "parent_hash": "<advisory>",
  "hmac": "<chain>"  // when RELIC_TRACE_KEY is set
}
```

Ordering guarantees:

- In the local `.trtrace` file, `IntentEvent` for `seq=N` is written
  strictly before `ActionEvent` for `seq=N`. This is verified by the
  runtime's `TestMCPProxy_IntentEventPrecedesActionEvent`.
- The HMAC chain is type-agnostic: sealed IntentEvents extend the
  same chain as RunEvent / ActionEvent / PolicyReloadEvent.
- Streaming ordering across the network is best-effort. The disk
  trace is the source of truth.

## Streaming flush + SSE shapes (slice 14)

There are two distinct SSE channels in the platform:

| Channel | Audience | Auth | Event shapes | Ships in |
|---|---|---|---|---|
| `POST /v1/intents` (HTTP ingest, not SSE) | Runtimes pushing live events | Org-scoped API key (`rk_…`) | One sealed `intent` or `action` line per request | Slice 14 |
| `GET /v1/orgs/:id/live` (SSE) | Dashboards / humans | User JWT, path org must match JWT org | `{org_id, type, agent, run, verdict, payload}` per frame | Slice 14 |
| `GET /v1/agents/:id/policy_updates` (SSE) | Agent runtimes | Org-scoped API key | "policy update available" notifications | Slice 15 (reserved) |

The dashboard channel filters by selector via query params
(`agent_name`, `tool`, `verdict`); cross-tenant isolation is enforced
server-side from the auth context.

## Policy set lifecycle (slice 15)

A policy_set is the editing unit: a YAML body + a selector that
resolves at write time to an agent set. Lifecycle:

1. **Author** — the dashboard's policy editor takes a selector
   (`name:X` or `env=prod, tier=primary`), previews the matched agent
   set, runs the diff badge against a representative agent's history,
   and posts to `POST /v1/policy_sets`.
2. **Persist** — the platform parses + validates the YAML, computes
   `policy_hash = sha256(yaml)[:8]` (matching the runtime), upserts
   the row by `(org_id, name)`, bumps `version`.
3. **Resolve** — `ResolveSelector(org, selector)` returns the agent
   set the selector matches *right now*. Selector evaluation is
   live: an agent that gains the matching label after the set is
   written will be picked up on the next selector resolution.
4. **Notify** — the platform publishes one `policyfeed.Notification`
   per matched agent on Postgres channel `relic_policy_updates`.
5. **Pull + apply** — each agent's `--watch` subscriber receives the
   notification, calls `client.PullPolicy(agent_name)`, validates,
   and calls `eng.SwapPolicy(parsed)`. The per-run HMAC chain key is
   not rotated.
6. **Report** — the runtime POSTs `/v1/agents/:name/policy_applied`
   with the applied hash. The platform stores it in
   `agents.applied_policy_hash` + `applied_at`.
7. **Render** — the dashboard reads `applied_policy_hash` against the
   set's `policy_hash` to render "47/52 on hash abc123, 5 stale".

## Hot-reload invariants (slice 15)

Two invariants the runtime preserves across every `eng.SwapPolicy`:

1. **In-flight decisions complete under their starting policy.**
   `Engine.Evaluate` reads the current `*Policy` under an RLock,
   releases the lock, then calls the pure `Evaluate` on the
   captured pointer. `SwapPolicy` takes a write lock and publishes
   a new pointer; readers either see the old or new pointer, never
   a torn mixture. Proved by
   `policy.TestEngineSwapDuringInFlightEvaluate` under the race
   detector.

2. **The per-run HMAC trace chain key is not rotated on hot reload.**
   The chain key is derived once at run start via
   `trace.GenerateChainKey(runID, masterSecret)` and bound to the
   trace writer. `SwapPolicy` touches only the policy pointer.
   A run started under v1 keeps its chain key when it hot-reloads
   to v2; `relic trace verify` (and the platform's
   `ParseAndVerify`) read the whole trace end-to-end and confirm
   the chain is intact.

Don't pre-populate; populate when the slice that defines the contract
actually ships.
