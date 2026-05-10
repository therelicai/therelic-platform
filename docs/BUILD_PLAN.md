# The Relic — Master Build Plan

> Private build plan covering all three repositories.
> See also: [Three-Repo Architecture Plan](/.cursor/plans/three-repo_architecture_plan_4a25b94f.plan.md)

---

## Completed Phases (in `therelicai/therelic`)

### Phase 1: Working Proxy (Weeks 1-3) — Complete

`relic run` intercepts MCP tool calls, records them, displays them. MCP proxy (stdio + HTTP+SSE), trace writer, MCP server aggregation.

### Phase 2: Authorization (Weeks 4-6) — Complete

Policy engine enforces default-deny. HTTP logger captures metadata. Redaction engine. `relic trace list/search`.

### Phase 3: Advanced Security + Identity (Weeks 7-8) — Complete

Behavioral contracts, capability fingerprinting, agent identity manifest, immutable policy history, delegation scope reduction.

### Phase 3b: Launch Prep (Weeks 9-10) — Complete

Cross-platform builds, integration tests, OpenClaw skill, policy hot-reload, docs, license structure, launch.

---

## Phase 4: Control Plane (Weeks 11-17)

**Repo:** `therelicai/therelic-platform`

| Week | Build | Done When |
|---|---|---|
| 11 | BSL 1.1 license, repo scaffold, Supabase project | Repo exists with LICENSE (BSL 1.1), NOTICE, TRADEMARKS.md, go.mod, docker-compose |
| 11-12 | Control Plane API (Go service) | POST/GET traces endpoints working. Auth via API keys. R2 storage. Postgres index. Agent registration. |
| 12-13 | `relic trace push` (in `therelic`) | CLI uploads traces to control plane API. On missing API key, prints signup URL. |
| 13-14 | Web UI — run list and trace viewer (`therelic-app`) | Browse runs, view trace events, filter by agent/env/denials. |
| 14-15 | Auth and org management | Supabase Auth integration. Org creation. Signup flow. API key management. |
| 15-16 | Billing | Stripe integration. Free tier (7-day retention). Team tier ($50/mo, 30-day). |
| 16-17 | Cross-site nav and polish | Shared nav. End-to-end user journey tested. |

### Phase 5: Governance Agents (Weeks 18-23)

**Repo:** `therelicai/therelic-platform` + `therelicai/therelic-app`

| Week | Build | Done When |
|---|---|---|
| 18-19 | Governance agent service | Go background worker. Polls API for new runs. Downloads traces. Denial pattern detection. |
| 19-20 | Intent classification | LLM integration (Claude API). Classifies denied actions. |
| 20-21 | Policy proposals data model and API | PolicyProposal table. GET/POST `/v1/proposals` endpoints. |
| 21-22 | Proposals UI (in `therelic-app`) | `/proposals` page. Approve/reject. Notification badge. Inline in trace viewer. |
| 22-23 | Notification channels | Email via Resend. Configurable frequency. |

### Phase 6: Trust Network + Marketplace (Demand-Driven)

**Repo:** `therelicai/therelic-platform` + `therelicai/therelic-app`

Not scheduled. Built when platform has 100+ orgs. Sequence:

1. Network mediation (mTLS transport binding, `from_agent` policy field)
2. Capability registry (listings table, registry API, MCP discovery server)
3. Trust scoring (daily batch job, weighted behavioral signals)
4. Bilateral policy generation (caller/provider templates, approval flow)
5. Metered transactions (transaction ledger, Stripe Connect settlement)
6. Marketplace UI (discovery, listings, trust profiles, transaction history)
