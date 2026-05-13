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
| `therelic-platform` | Hosted API (Go, Postgres, S3). Multi-tenant brain. No UI. | BSL 1.1 → Apache 2.0 | `therelic`, `therelic-app` |
| `therelic-app` | Web console (React/TypeScript). Owns presentation, no business logic. | BSL 1.1 → Apache 2.0 | `therelic-platform` |
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

---

## Selector contract

Every API contract that targets agents accepts a **selector**, not a raw
agent name — even when the selector resolves to a single agent today. This
is forward-compatible with slice 15's label-match grammar.

```json
// Slice 13: only the single-agent form is valid.
{ "agent_selector": { "agent_name": "code-assist" } }
```

```jsonc
// Slice 15 will extend (additive, non-breaking):
//   { "agent_selector": { "match": { "env": "prod" } } }
// Both arms remain valid; the simple form is not deprecated.
```

The platform's selector resolver for slice 13 is exactly one line:
`WHERE agent_name = $1`. Empty selectors are rejected with `400 Bad Request`.

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
| `policy_reload` | pre-slice-13 | Frozen |
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

## Reserved sections (populated in later slices)

- **Policy set lifecycle** — slice 15
- **Hot-reload invariants (HMAC chain + in-flight decisions)** — slice 15
- **Label-match selector grammar** — slice 15

Don't pre-populate; populate when the slice that defines the contract
actually ships.
