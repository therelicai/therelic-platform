# The Relic Platform — Technical Architecture

> Private architecture documentation for the hosted platform (BSL 1.1 licensed).
> Covers the control plane, governance agents, trust network, and enterprise extensions.
>
> The open-source mediation layer (Sections 0-9) is documented in `therelicai/therelic`.

---

## 10. Control Plane

The control plane is the authority for four functions:

- **Trace storage and analysis:** Accepts uploads, S3 storage, Postgres index.
- **Policy distribution:** Source of truth for policies. Agents pull current policy. Local files as fallback.
- **Agent registration:** Identity manifests registered. Control plane issues Ed25519 signatures binding identity to org.
- **Behavioral baselines:** Per-agent baselines computed from trace data. Stored in Postgres.

### 10.1 Architecture

```
┌──────────────┐      ┌────────────────────────────────┐
│ relic trace push│─────>│     The Relic Control Plane    │
│  (from CLI)  │      │     (single Go service)         │
└──────────────┘      │                                 │
                      │  POST /traces  ──> S3           │
┌──────────────┐      │  GET  /traces  ──> Postgres     │
│   Web UI     │<────>│  GET  /traces/:id ──> S3        │
│  (React SPA) │      │                                 │
└──────────────┘      │  Policy Authority               │
                      │  Agent Registry                 │
                      │  Auth: Supabase Auth            │
                      │  Billing: Stripe                │
                      └──────────┬──────────────────────┘
                                 │
                    ┌────────────┼────────────┐
                    │            │            │
              ┌─────▼────┐ ┌────▼───┐ ┌──────▼─────┐
              │ Postgres  │ │  S3/R2 │ │ Background │
              │           │ │        │ │  Worker    │
              │ runs index│ │ traces │ │            │
              │ orgs/users│ │ (gzip) │ │ retention  │
              │ api_keys  │ │        │ │ metering   │
              │ agents    │ │        │ │ baselines  │
              │ baselines │ │        │ │            │
              └───────────┘ └────────┘ └────────────┘
```

### 10.2 API

Base: `https://api.therelic.dev/v1` Auth: Bearer token (org-scoped API key) or Supabase Auth JWT.

**Trace and org management:**

| Method | Path | Description |
|---|---|---|
| `POST` | `/traces` | Upload trace file |
| `GET` | `/traces` | List runs (filtered, paginated) |
| `GET` | `/traces/:run_id` | Get run metadata |
| `GET` | `/traces/:run_id/events` | Download and parse trace file |
| `DELETE` | `/traces/:run_id` | Delete a run |
| `POST` | `/orgs` | Create organization |
| `GET` | `/orgs/:id` | Get org details |
| `POST` | `/orgs/:id/api-keys` | Create API key |
| `DELETE` | `/orgs/:id/api-keys/:kid` | Revoke API key |
| `GET` | `/user` | Current user info |

**Agent registry and policy authority:**

| Method | Path | Description |
|---|---|---|
| `POST` | `/agents` | Register agent identity manifest |
| `GET` | `/agents` | List registered agents for org |
| `GET` | `/agents/:name` | Agent details + identity + baseline |
| `GET` | `/agents/:name/policy` | Policy distribution (agent pulls) |
| `PUT` | `/agents/:name/policy` | Policy authority (update) |
| `GET` | `/agents/:name/baseline` | Behavioral baseline |

### 10.3 Database (Postgres)

```sql
CREATE TABLE organizations (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name       TEXT NOT NULL,
    slug       TEXT UNIQUE NOT NULL,
    plan       TEXT NOT NULL DEFAULT 'free',
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE users (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL REFERENCES organizations(id),
    email      TEXT UNIQUE NOT NULL,
    role       TEXT NOT NULL DEFAULT 'member',
    created_at TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE api_keys (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     UUID NOT NULL REFERENCES organizations(id),
    key_hash   TEXT UNIQUE NOT NULL,
    key_prefix TEXT NOT NULL,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ DEFAULT now(),
    revoked_at TIMESTAMPTZ
);

CREATE TABLE runs (
    id              TEXT NOT NULL,
    org_id          UUID NOT NULL REFERENCES organizations(id),
    agent_name      TEXT NOT NULL,
    agent_version   TEXT NOT NULL,
    policy_hash     TEXT NOT NULL,
    environment     TEXT NOT NULL,
    started_at      TIMESTAMPTZ NOT NULL,
    duration_ms     INTEGER,
    exit_code       INTEGER,
    actions_total   INTEGER DEFAULT 0,
    actions_allowed INTEGER DEFAULT 0,
    actions_denied  INTEGER DEFAULT 0,
    storage_key     TEXT NOT NULL,
    expires_at      TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (org_id, id)
);

CREATE INDEX idx_runs_lookup ON runs(org_id, agent_name, started_at DESC);
CREATE INDEX idx_runs_expiry ON runs(expires_at) WHERE expires_at IS NOT NULL;
```

**Agent registry tables:**

```sql
CREATE TABLE agents (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id              UUID NOT NULL REFERENCES organizations(id),
    name                TEXT NOT NULL,
    version             TEXT NOT NULL,
    identity_manifest   JSONB NOT NULL,
    capabilities_hash   TEXT NOT NULL,
    policy_hash         TEXT NOT NULL,
    registered_at       TIMESTAMPTZ DEFAULT now(),
    last_seen           TIMESTAMPTZ DEFAULT now(),
    UNIQUE(org_id, name)
);

CREATE TABLE agent_baselines (
    agent_id            UUID NOT NULL REFERENCES agents(id),
    computed_at         TIMESTAMPTZ NOT NULL,
    window_days         INTEGER DEFAULT 30,
    avg_actions_per_run NUMERIC,
    avg_denials_per_run NUMERIC,
    tool_distribution   JSONB NOT NULL,
    PRIMARY KEY (agent_id, computed_at)
);
```

### 10.4 Trace Upload Flow

```bash
relic trace push <run_id>
# or
relic trace push --all --since 7d
```

1. CLI reads `.trtrace` file
2. Compresses with gzip
3. Extracts run metadata from first and last events
4. `POST /traces` with metadata headers and gzipped body
5. API validates, stores in S3, indexes run in Postgres
6. Returns confirmation with retention expiry

### 10.5 Infrastructure

| Component | Service | Monthly Cost (early) |
|---|---|---|
| API | Fly.io (1 machine, 1GB RAM) | $15 |
| Database | Supabase Postgres (free tier → $25) | $0–25 |
| Storage | Cloudflare R2 (zero egress) | $5–50 |
| Auth | Supabase Auth (free tier) | $0 |
| DNS/CDN | Cloudflare (free tier) | $0 |
| **Total** | | **$20–90/month** |

---

## 11. Governance Agents

The Relic's own agents that manage governance on behalf of users. This layer transforms The Relic from enforcement infrastructure into an intelligent governance platform.

The governance agent layer operates at two levels: local (via the proactive skill) and hosted (via the governance agent service).

### 11.1 Architecture

```
┌─────────────────────────┐
│   Governance Agent      │
│   (The Relic hosted)   │
│                         │
│   Reads traces via API  │
│   Detects patterns      │
│   Proposes policy changes│
└──────────┬──────────────┘
           │
┌──────────┼─────────────────┐
│          │                 │
┌──────▼──────┐  ┌──────▼──────┐  ┌───────▼───────┐
│ Control     │  │ Web App     │  │ Notifications │
│ Plane API   │  │             │  │               │
│             │  │ Proposals   │  │ Email, Slack  │
│ GET traces  │  │ Approve/    │  │ Webhooks      │
│ GET runs    │  │ Reject UI   │  │               │
└─────────────┘  └─────────────┘  └───────────────┘
```

The governance agent does NOT run on the user's machine. It runs on The Relic's infrastructure and accesses traces through the control plane API. This separation matters: the governance agent cannot be compromised by the user's agent, and it has a global view across all of a user's runs rather than only the current session.

The governance agent is a stateless consumer of the control plane. It reads traces, reads baselines, writes proposals, triggers notifications — all through control plane APIs. No own database connections.

### 11.2 Governance Agent Specification

**Input:** Traces from the control plane API. The governance agent reads runs via `GET /v1/traces` and events via `GET /v1/traces/:run_id/events`.

**Processing:**

1. **Denial pattern detection.** Group denied actions by tool name across recent runs. If a tool is denied more than N times (configurable, default 5) in a rolling window (configurable, default 7 days), generate a proposal.

2. **Intent analysis.** For denied actions, examine the parameters to determine likely intent. "`shell_exec` with params `npm install lodash`" is a package installation, not arbitrary code execution. The governance agent uses an LLM to classify denied actions into intent categories and assess whether the denial reflects correct policy or a gap.

3. **Behavioral drift detection.** Compare the current run's action profile (tool distribution, action count, timing) against the user's baseline (from control plane). Flag runs that deviate significantly. This catches both compromised agents and legitimate behavior changes that need policy updates.

4. **Policy gap vs correct denial.** The governance agent maintains a classification for each denial pattern:
   - **Correct denial:** The user intended to block this. No proposal generated.
   - **Policy gap:** The user likely wants this allowed but hasn't configured it. Proposal generated.
   - **Uncertain:** Not enough data. Track and revisit.

   Classification improves over time as the user approves or rejects proposals. Approved proposals train the model toward identifying similar patterns as gaps. Rejected proposals train it toward identifying similar patterns as correct denials.

**Output:** Policy proposals.

### 11.3 Policy Proposal Data Model

```
PolicyProposal {
    id:         ULID
    org_id:     UUID
    agent_name: string
    status:     enum { pending, approved, rejected, expired }

    trigger: {
        type:     enum { denial_pattern, behavioral_drift, optimization }
        evidence: {
            run_ids:                []string
            denied_tool:            string
            denial_count:           int
            sample_params:          []json
            intent_classification:  string
        }
    }

    proposed_rule: {
        id:          string
        protocol:    string
        method:      string
        target:      string
        action:      string
        explanation: string
    }

    created_at:  timestamp
    decided_at:  timestamp?
    decided_by:  string?
}
```

### 11.4 Policy Proposals API

| Method | Path | Description |
|---|---|---|
| `GET` | `/proposals` | List proposals (filtered by status, agent) |
| `GET` | `/proposals/:id` | Get proposal details |
| `POST` | `/proposals/:id/approve` | Approve — applies rule to policy |
| `POST` | `/proposals/:id/reject` | Reject — marks as correct denial |
| `DELETE` | `/proposals/:id` | Dismiss without feedback |

On approval, the control plane:

1. Fetches the current policy for the agent
2. Inserts the proposed rule at the appropriate position
3. Returns the updated policy YAML
4. Optionally pushes the updated policy via webhook or `relic policy pull`

### 11.5 Policy Proposals UI

In the web application:

- `/proposals` — list of pending proposals, grouped by agent. Each shows: agent name, proposed rule, trigger evidence, explanation, approve/reject buttons.
- Notification badge on nav when proposals are pending.
- Inline in the trace viewer: when viewing a run with denials that triggered a proposal, the proposal is shown contextually next to the denied action.

### 11.6 Notification Channels

| Channel | Implementation | Timeline |
|---|---|---|
| In-app | Notification badge + proposals page | With governance agent v1 |
| Email | Transactional email via Resend | With governance agent v1 |
| Slack | Slack webhook integration | Post-launch |
| Custom webhook | POST to user-configured URL | Post-launch |

Notification frequency is configurable per org: immediate, daily digest, or weekly digest.

### 11.7 Specialized Agents (Later)

**Anomaly detection agent.** Statistical analysis across runs. Builds a baseline profile per agent. Flags runs that deviate beyond a configurable threshold.

**Compliance reporting agent.** Reads traces and generates reports in compliance frameworks (SOC 2, SOX, HIPAA). Produces: attestation of policy enforcement, summary of all denied actions with rationale, data access log, retention compliance verification.

**Policy optimization agent.** Analyzes policy effectiveness across an organization. Identifies: redundant rules, overly permissive rules, inconsistent policies across agents, and consolidation opportunities.

### 11.8 Governance Agent Technology

The governance agent is a Go service that:

1. Polls the control plane API for new runs (or receives webhooks)
2. Downloads and parses traces
3. Runs pattern detection (denial grouping, behavioral comparison against baselines)
4. Calls an LLM (Claude via Anthropic API) for intent classification and proposal generation
5. Writes proposals to Postgres via the control plane API

It runs as a background worker on Fly.io alongside the control plane API.

---

## 12. Trust Network

The long-term vision: agents discover, authorize, and transact with each other through governed MCP connections across organizational boundaries. This is the foundation of the non-frontier agentic marketplace.

### 12.1 Architecture

```
Organization A               Organization B
┌──────────────────┐          ┌──────────────────┐
│ Agent A           │          │ Agent B           │
│ (research agent)  │          │ (data science)    │
└────────┬─────────┘          └────────┬─────────┘
         │                             │
┌────────▼─────────┐          ┌────────▼─────────┐
│ The Relic        │◄── MCP/mTLS ──►│ The Relic        │
│ (Org A proxy)     │          │ (Org B proxy)     │
│                   │          │                   │
│ Policy: A→B rules │          │ Policy: B→A rules │
│ Trace: A's actions│          │ Trace: B's actions│
└────────┬─────────┘          └────────┬─────────┘
         │                             │
         └──────────────┬──────────────┘
                        │
              ┌─────────▼──────────┐
              │ The Relic Platform │
              │                    │
              │ Capability Registry│
              │ Trust Scoring      │
              │ Bilateral Policies │
              │ Transaction Ledger │
              └────────────────────┘
```

### 12.2 Network Mediation

The same mediation engine with a network-facing transport binding:

**Transport:** MCP over HTTPS with mutual TLS (mTLS) or API key authentication.

**Authentication:** The calling agent's proxy presents credentials that identify the calling organization and agent. The receiving proxy verifies the identity against the control plane agent registry.

**Policy evaluation:** Identical to local proxy. Same engine, same YAML format, same glob matching. Rules can reference remote agent identities:

```yaml
rules:
  - id: allow-analyze-from-org-a
    protocol: mcp
    method: tool_call
    target: "analyze_dataset"
    action: allow
    from_agent: "org-a/research-agent"
```

**Trace correlation:** Both sides record the interaction. The calling proxy writes with `to_agent` populated. The receiving proxy writes with `from_agent` populated. Both share a `corr` ID.

### 12.3 Capability Registry

Agents publish their available tools as MCP tool definitions:

```
CapabilityListing {
    agent_id:    string
    org_id:      UUID
    endpoint:    URL
    tools:       []ToolDefinition
    trust_score: float          // 0.0 - 1.0
    pricing: {
        model:              enum { free, per_call, subscription }
        per_call_price:     decimal?
        subscription_price: decimal?
    }
    created_at:  timestamp
    last_active: timestamp
}
```

**Registry API:**

| Method | Path | Description |
|---|---|---|
| `GET` | `/registry` | Search capabilities (by tool name, category) |
| `POST` | `/registry` | Publish capability listing |
| `PUT` | `/registry/:agent_id` | Update listing |
| `DELETE` | `/registry/:agent_id` | Remove listing |
| `GET` | `/registry/:agent_id/trust` | Get trust score details |

**Discovery via MCP.** The registry itself is exposed as an MCP server. An agent governed by The Relic can search the registry using standard MCP tool calls — `registry_search`, `registry_get_trust_score`. Discovery is itself governed and traced.

### 12.4 Trust Scoring

Trust scores are derived from trace data:

| Signal | Weight | Source |
|---|---|---|
| Total successful interactions | High | Trace events with `auth: allow` and successful result |
| Policy violation rate | High (inverse) | Trace events with `auth: deny` as fraction of total |
| Uptime / availability | Medium | Registry heartbeat + successful connection rate |
| Response latency | Low | Trace event `ms` field |
| Account age | Low | Organization creation date |
| Verification status | Bonus | Manual verification by The Relic team |

**Score calculation:** Weighted average, normalized to 0.0–1.0. Recalculated daily from the trailing 30-day trace window. Agents with fewer than 10 interactions show "Unrated."

**Score decay:** Inactive agents decay toward 0.5 over 30 days.

### 12.5 Bilateral Policy Templates

When Agent A wants to use Agent B's capabilities, the platform generates bilateral policies for both sides. Both presented for approval. Neither side sees the other's full policy.

### 12.6 Metered Transactions

```
Transaction {
    id:              ULID
    corr:            string
    caller_org_id:   UUID
    provider_org_id: UUID
    caller_agent:    string
    provider_agent:  string
    tool:            string
    duration_ms:     int
    price:           decimal
    status:          enum { completed, failed, disputed }
    created_at:      timestamp
}
```

**Settlement:** Daily aggregation. Monthly payouts via Stripe Connect. Platform fee 5–15%.

### 12.7 Trust Network Data Model (Postgres)

```sql
CREATE TABLE capability_listings (
    agent_id    TEXT PRIMARY KEY,
    org_id      UUID NOT NULL REFERENCES organizations(id),
    endpoint    TEXT NOT NULL,
    tools       JSONB NOT NULL,
    trust_score NUMERIC(3,2) DEFAULT 0.50,
    pricing     JSONB NOT NULL DEFAULT '{"model":"free"}',
    created_at  TIMESTAMPTZ DEFAULT now(),
    last_active TIMESTAMPTZ DEFAULT now()
);

CREATE TABLE bilateral_agreements (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    caller_org_id   UUID NOT NULL REFERENCES organizations(id),
    provider_org_id UUID NOT NULL REFERENCES organizations(id),
    caller_agent    TEXT NOT NULL,
    provider_agent  TEXT NOT NULL,
    caller_policy   JSONB NOT NULL,
    provider_policy JSONB NOT NULL,
    status          TEXT NOT NULL DEFAULT 'pending',
    created_at      TIMESTAMPTZ DEFAULT now(),
    approved_at     TIMESTAMPTZ
);

CREATE TABLE transactions (
    id              TEXT PRIMARY KEY,
    corr            TEXT NOT NULL,
    caller_org_id   UUID NOT NULL REFERENCES organizations(id),
    provider_org_id UUID NOT NULL REFERENCES organizations(id),
    caller_agent    TEXT NOT NULL,
    provider_agent  TEXT NOT NULL,
    tool            TEXT NOT NULL,
    duration_ms     INTEGER,
    price           NUMERIC(10,4),
    status          TEXT NOT NULL DEFAULT 'completed',
    created_at      TIMESTAMPTZ DEFAULT now()
);

CREATE INDEX idx_transactions_caller ON transactions(caller_org_id, created_at DESC);
CREATE INDEX idx_transactions_provider ON transactions(provider_org_id, created_at DESC);
CREATE INDEX idx_capability_search ON capability_listings USING gin(tools);
```

---

## 13. Enterprise Extensions (Deferred)

These features are specified at the design level only. Built when customers with signed contracts or LOIs require them.

### 13.1 Cryptographic Agent Identity (Ed25519 CA)

Full Ed25519 organizational CA. Agent identities signed by org CA. Runtime verifies at startup. Enables identity-based policy resolution and cross-organization trust.

### 13.2 Policy Registry

Centralized policy storage with version history, approval workflows, and environment-specific deployment. Governance agent proposals applied directly to the registry.

### 13.3 Capability Verification

MCP server packages signed and version-pinned. Runtime verifies signature before proxying.

### 13.4 Compliance Engine

Audit export in SOC 2 / HIPAA / custom formats. SIEM integration via OTEL / webhook / syslog. Configurable retention policies with legal hold.

### 13.5 Full HTTPS Inspection

Local CA generation, TLS MITM, path-level HTTP authorization. Requires CA injection into agent runtime trust store.

### 13.6 Filesystem and Subprocess Monitoring

File operation and child process interception via inotify/FSEvents and process tree monitoring. Normalized to the same `ActionIntent` model.

---

## 14. Technology Choices (Platform)

| Component | Choice | Why |
|---|---|---|
| Hosted API | Go + `chi` router | Lightweight, standard |
| Database | Supabase Postgres | Managed, auth integration, RLS |
| Storage | Cloudflare R2 | S3-compatible, zero egress fees |
| Web UI | React + TypeScript + Vite + Tailwind | Standard SPA stack |
| Auth | Supabase Auth | Native DB integration, one fewer vendor |
| Billing | Stripe | Don't build billing |
| Hosting | Fly.io | Simple deploy, scales, cheap |
| Agent identity (hosted) | Ed25519 | Standard for org CA, compact signatures |
| Governance agent LLM | Anthropic Claude API | Intent classification, proposal generation |
| Governance agent runtime | Go background worker | Same stack as API, deployed alongside |
| Transactional email | Resend | Simple API, good deliverability |
| Trust network auth | mTLS or API keys | Standard mutual authentication |
| Marketplace payouts | Stripe Connect | Don't build payment infrastructure |
