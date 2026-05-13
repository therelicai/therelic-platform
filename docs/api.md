# therelic-platform API reference

This is the authoritative endpoint reference for the platform. All API
surfaces ship here; the [README](../README.md) and
[ARCHITECTURE.md](./ARCHITECTURE.md) link to this file and do not
duplicate endpoint detail.

The platform is multi-tenant. Every `/v1` endpoint requires authentication
via either an `Authorization: Bearer <api_key>` header (server-to-server)
or a Supabase JWT (web client). The auth layer resolves the request to an
`org_id`; missing or mismatched org context returns `403`.

Cross-repo contracts (selector shape, event types, replay protocol) are
defined in [RELIC.md](../RELIC.md). When this document and RELIC.md
disagree, RELIC.md wins; open a PR to fix the drift.

---

## Conventions

- All request and response bodies are `application/json` unless explicitly
  noted (trace upload is gzipped NDJSON; trace download is gzipped NDJSON).
- All timestamps are RFC 3339 in UTC.
- Errors follow `{ "error": "<message>" }` and an appropriate HTTP status.

---

## Health

### GET `/health`

Liveness probe. Returns `200 {"status":"ok"}` if the process is up. Does
not check downstream dependencies.

### GET `/readyz`

Readiness probe. Verifies Postgres + S3 reachability with a 3-second
timeout. Returns `200` with `{ status: "ok", checks: {...} }` when both are
healthy, `503` with `{ status: "degraded", ... }` otherwise. Use this for
load-balancer health checks; `/health` is for process supervision.

---

## Policy simulator (slice 13)

The simulator answers "if I deployed this YAML across the selected agents
for the last N days, how would historical verdicts have changed?" It is
read-only and runs the same `Engine.Evaluate` code the runtime uses to
enforce. See [RELIC.md → Replay protocol](../RELIC.md#replay-protocol-slice-13)
for the cross-repo contract.

### POST `/v1/policy/simulate`

Queue an async simulation job. The candidate policy is parsed and
validated synchronously, so a YAML typo comes back as a `400` from this
call (not as a follow-up `status: error`).

**Request:**

```json
{
  "policy_yaml": "version: \"1\"\nagent:\n  name: code-assist\nmode: enforce\ndefault: deny\nrules:\n  - id: allow-search\n    protocol: mcp\n    method: tool_call\n    target: web_search\n    action: allow\n",
  "agent_selector": { "agent_name": "code-assist" },
  "window_days": 7
}
```

| Field | Required | Notes |
|---|---|---|
| `policy_yaml` | yes | The candidate policy. Body capped at 64 KiB. |
| `agent_selector` | yes | Selector contract (see [RELIC.md](../RELIC.md#selector-contract)). Slice 13 only accepts `{ "agent_name": "..." }`; empty rejects with 400. |
| `window_days` | yes | One of `1`, `7`, `30`. |

**Responses:**

- `202 Accepted` — `{ "job_id": "<ulid>" }`
- `400 Bad Request` — selector missing, policy invalid, window unsupported, body malformed
- `401 Unauthorized` — missing/invalid credentials
- `413 Payload Too Large` — body exceeds 64 KiB
- `503 Service Unavailable` — simulator not configured (the API booted without S3)

### GET `/v1/policy/simulate/:job_id`

Poll a previously-submitted job. The job is org-scoped: a job submitted by
org A cannot be polled by org B (returns `404`).

**Response (terminal):**

```json
{
  "job_id": "01J…",
  "status": "done",
  "submitted_at": "2026-05-01T12:00:00Z",
  "finished_at": "2026-05-01T12:00:02Z",
  "result": {
    "newly_denied": 4,
    "newly_allowed": 0,
    "unchanged": 120,
    "total_evaluated": 124,
    "runs_scanned": 18,
    "samples": [
      {
        "run_id": "01J…",
        "target": "exec_shell",
        "method": "tool_call",
        "old_auth": "allow",
        "new_auth": "deny"
      }
    ]
  }
}
```

| Status | Meaning |
|---|---|
| `pending` | Job accepted, worker hasn't picked it up. |
| `running` | Worker is downloading traces and evaluating. |
| `done` | Result is populated. |
| `error` | `error` field carries a human-readable reason. |

Samples are capped at 5 per direction (newly_denied / newly_allowed).
Results are kept in-memory; an API restart loses state and clients should
re-submit.

---

## Traces

### POST `/v1/traces`

Upload a gzipped NDJSON trace. The platform parses server-side and
recomputes the run summary from the events themselves — client-supplied
headers are advisory at best. Caps the body at 100 MiB.

When `RELIC_TRACE_KEY` is set on the server, the HMAC chain is
recomputed against the per-run key derived from the master secret.
`RELIC_REQUIRE_SEALED_TRACES=1` additionally rejects unsealed uploads
(`422`).

| Status | Meaning |
|---|---|
| `201 Created` | Trace indexed. Returns `run_id`, action counts, integrity flags. |
| `200 OK` | Idempotent re-upload — returns existing run identity. |
| `400 Bad Request` | Malformed gzip, missing run-start, empty events. |
| `413 Payload Too Large` | >100 MiB. |
| `422 Unprocessable Entity` | Chain verification failed or required chain missing. |

### GET `/v1/traces`

List runs for the calling org, ordered by `started_at` desc. Filter by
agent with `?agent=<name>`. Pagination via `limit` (max 100, default 50)
and `offset`.

### GET `/v1/traces/:run_id`

Single run metadata.

### GET `/v1/traces/:run_id/events`

Stream the original gzipped NDJSON. Used by the trace viewer and by the
simulator's internal trace fetch.

### DELETE `/v1/traces/:run_id`

Removes the run from the index and deletes the S3 object.

---

## Agents

### POST `/v1/agents`

Register or update an agent. Body: `{ name, version, identity_manifest, capabilities_hash, policy_hash }`.

### GET `/v1/agents`, `GET /v1/agents/:name`

List or fetch a single agent.

### GET `/v1/agents/:name/baseline`

Returns the agent's behavioral baseline (computed by the governance
worker): rolling window, average actions/denials per run, tool
distribution.

### GET `/v1/agents/:name/policy`, `PUT /v1/agents/:name/policy`

Fetch or replace the policy YAML for the agent.

---

## Organizations & API keys

### POST `/v1/onboard`

Idempotent org bootstrap for a new user. Creates the org + initial API
key.

### POST `/v1/orgs`, `GET /v1/orgs/:org_id`

Org admin operations.

### POST `/v1/orgs/:org_id/api-keys`, `DELETE /v1/orgs/:org_id/api-keys/:key_id`

Create / revoke API keys. New keys are stored as
`HMAC-SHA256(pepper, key)` when `RELIC_API_KEY_PEPPER` is set, otherwise
as `SHA-256(key)` (legacy compatibility path).

---

## User

### GET `/v1/user`

Returns the calling user's id, org, and email (when authenticated via JWT).

---

## Audit

### GET `/v1/audit-events`

List of audit events for the calling org. Pagination via `limit` / `offset`.

---

## Proposals

### GET `/v1/proposals`, `GET /v1/proposals/:id`

List or fetch governance proposals. Filter by `?status=...`.

### POST `/v1/proposals/:id/approve`, `POST /v1/proposals/:id/reject`, `DELETE /v1/proposals/:id`

Decision flow for individual proposals.

---

## Registry & Transactions

Endpoints for the Trust Network registry and ledger transactions. See the
relevant handlers in [internal/api/registry.go](../internal/api/registry.go)
and [internal/api/transactions.go](../internal/api/transactions.go). These
predate slice 13 and are scoped to the marketplace surface; the slice 13
documentation block does not add to them.

---

## Live feed (slice 14)

The Live view is powered by two endpoints: runtimes POST sealed events
as they happen, and the dashboard subscribes to a server-sent stream
that fans them out. See [RELIC.md → Intent event contract](../RELIC.md#intent-event-contract-slice-14)
for the wire-format definition and ordering guarantees.

### POST `/v1/intents`

Push a single sealed `intent` or `action` event from the runtime's
streaming flush path. Authenticated with an org-scoped API key
(`rk_…`); the org_id in the auth context is the tenant on whose live
channel the event will appear.

**Request:**

```
POST /v1/intents
Authorization: Bearer rk_…
Content-Type: application/x-ndjson

{"v":1,"t":"intent","ts":"…","run":"…","seq":42,"proto":"mcp",…,"hmac":"…"}
```

| Constraint | Value |
|---|---|
| Body cap | 32 KiB |
| Accepted `t` values | `intent`, `action` |
| Payload limit downstream (Postgres NOTIFY) | 8 KiB raw JSON — the platform rejects larger envelopes with 500 |

**Responses:**

- `202 Accepted` — event published to the live feed.
- `400 Bad Request` — empty body, malformed JSON, or unsupported event type.
- `401 Unauthorized` — missing or invalid API key.
- `413 Payload Too Large` — body > 32 KiB.
- `503 Service Unavailable` — live feed not configured (e.g., the platform booted without a working Postgres LISTEN connection).

Side effects: every successful publish calls
`UpdateAgentLastSeen(org, resolved_agent)` so the dashboard's Online
pill remains accurate without waiting for a batch trace upload.

### GET `/v1/orgs/:orgID/live`

Dashboard-facing SSE channel. Authenticated by user JWT (Supabase
Auth). The `orgID` in the path **must** match the JWT's resolved
org — a mismatch returns 403 so a leaked token can't peek at another
tenant by varying the URL.

**Query parameters (all optional):**

| Param | Meaning |
|---|---|
| `agent_name` | Restrict to events emitted by this agent name. |
| `tool` | Restrict to events whose `target` is this tool. |
| `verdict` | Restrict to action events with this `auth` value (`allow`, `deny`, `audit_deny`, `would_deny`). |

Slice 15 extends the selector grammar to label-match expressions. The
existing query params remain valid.

**Response:**

- `200 OK` with `Content-Type: text/event-stream`.
- A `retry: 2000` line for client-side reconnect hints.
- Comment frames (`: ping`) every 25 seconds to keep idle proxies from closing the TCP connection.

**Event frame format:**

```
event: intent
data: {"org_id":"…","type":"intent","agent":"code-assist","run":"01J…","verdict":"","payload":{...full sealed event...}}

event: action
data: {"org_id":"…","type":"action","agent":"code-assist","run":"01J…","verdict":"deny","payload":{...full sealed event...}}
```

The `payload` is the runtime's sealed event line exactly as it would
appear in the `.trtrace` file. The convenience fields (`agent`, `run`,
`verdict`) are extracted at publish time so subscribers can filter or
render without re-parsing the payload.

**Audience distinction:** this channel is for dashboards. The
agent-facing channel `GET /v1/agents/:id/policy_updates` planned for
slice 15 is a separate endpoint with different auth (org-scoped API
key), different audience (runtime processes), and different event
shapes (policy update notifications, not action stream).

---

## Reserved (populated by later slices)

- `POST/PUT /v1/policy_sets`, `POST /v1/agents/:id/labels`, `GET /v1/agents/:id/policy_updates` (SSE, agent), `POST /v1/agents/:id/policy_applied` — slice 15
