# The Relic Platform

**The hosted control plane for [The Relic](https://github.com/therelicai/therelic) — trace storage, governance agents, policy authority, and billing.**

This is the server-side brain of the Relic ecosystem. It receives trace uploads from the open-source CLI, indexes them in Postgres, stores raw events in S3-compatible object storage, runs autonomous governance agents that detect policy gaps, and serves as the policy authority that agents pull from at runtime.

> **License:** Business Source License 1.1 (BSL 1.1). Source-available, not
> OSI-open. Self-host for any purpose — internal production use is
> explicitly permitted by the Additional Use Grant — but you may not run
> a hosted Governance Service that competes with `api.therelic.dev`.
> Each released file converts to Apache License 2.0 four years after
> publication. See [LICENSE](./LICENSE), [NOTICE](./NOTICE), and
> [TRADEMARKS.md](./TRADEMARKS.md).

---

## How It Fits Into The Relic Ecosystem

```
┌─────────────────────────────────────────────────────────────┐
│  Open Source (Apache 2.0) — github.com/therelicai/therelic   │
│                                                              │
│  relic CLI / MCP Proxy / Policy Engine / Trace Writer        │
│  Runs on the user's machine. Governs AI agent actions.       │
└──────────────────┬───────────────────────────────────────────┘
                   │  HTTP (relic trace push, relic policy pull)
                   ▼
┌─────────────────────────────────────────────────────────────┐
│  This Repo — therelic-platform                               │
│                                                              │
│  Control Plane API (Go) — accepts traces, distributes policy │
│  Governance Worker — detects denial patterns, classifies     │
│    intent with LLM, generates policy proposals               │
│  Postgres — orgs, users, runs, agents, proposals, baselines  │
│  S3/R2 — raw trace event storage (gzipped NDJSON)            │
│  Stripe — billing and usage metering                         │
└──────────────────┬───────────────────────────────────────────┘
                   │  REST /v1/*
                   ▼
┌─────────────────────────────────────────────────────────────┐
│  therelic-app — React SPA dashboard                          │
│  therelic-website — Marketing site (therelic.dev)             │
└─────────────────────────────────────────────────────────────┘
```

The open-source CLI and the platform communicate **only over HTTP** — there is no shared Go import. The CLI works entirely standalone with local traces and local policy. The platform is an optional upgrade that adds cloud storage, team collaboration, governance automation, and the marketplace.

---

## Architecture

### Two Services

| Service | Entry Point | Purpose |
|---|---|---|
| **relic-api** | `cmd/relic-api/main.go` | HTTP control plane serving all REST endpoints |
| **relic-governance** | `cmd/relic-governance/main.go` | Background worker that polls for denial patterns and generates proposals |

### API Endpoints

Base: `https://api.therelic.dev/v1` — Auth via Bearer JWT (Supabase Auth) or org-scoped API key.

| Group | Endpoints | Description |
|---|---|---|
| **Traces** | `POST /traces`, `GET /traces`, `GET /traces/:id`, `GET /traces/:id/events`, `DELETE /traces/:id` | Upload, list, view, download, and delete trace runs |
| **Agents** | `POST /agents`, `GET /agents`, `GET /agents/:name`, `GET /agents/:name/policy`, `PUT /agents/:name/policy`, `GET /agents/:name/baseline` | Agent registry, policy distribution, behavioral baselines |
| **Organizations** | `POST /orgs`, `GET /orgs/:id`, `POST /orgs/:id/api-keys`, `DELETE /orgs/:id/api-keys/:kid` | Org management and API key lifecycle |
| **Proposals** | `GET /proposals`, `GET /proposals/:id`, `POST /proposals/:id/approve`, `POST /proposals/:id/reject`, `DELETE /proposals/:id` | Governance proposals from automated detection |
| **Audit** | `GET /audit-events` | Platform-level audit trail |
| **Onboarding** | `POST /onboard` | Explicit org creation for new signups |
| **Registry** | `GET /registry`, `POST /registry`, `PUT /registry/:agentID`, `GET /registry/:agentID/trust` | Trust network and marketplace (future) |
| **Transactions** | `GET /transactions`, `GET /transactions/summary` | Metered agent-to-agent transactions (future) |

### Middleware

- **CORS** — Allows `app.therelic.dev`, `localhost:5173`, `localhost:5174`
- **Rate limiting** — Token bucket per IP (10 req/s, burst 20)
- **Auth** — JWT validation (Supabase) or API key hash lookup
- **Request logging** — Structured JSON via `slog`

### Governance Engine

The governance worker runs on a 60-second poll interval:

1. **Detection** — For each org, scans recent runs with denials. Downloads gzipped NDJSON traces from S3 to identify which specific tools were denied (not just aggregate counts).
2. **Classification** — Sends denied tool + parameters to Claude for intent classification: is this a policy gap (user wants it allowed) or a correct denial (intentionally blocked)?
3. **Proposal generation** — For classified gaps, creates a governance proposal with the suggested policy rule, evidence (run IDs, denial counts), and the LLM's reasoning.

Proposals appear in the dashboard for human review (approve/reject/dismiss).

### Database Schema

7 migrations in `migrations/`:

| Migration | Tables |
|---|---|
| 001 | `organizations`, `users`, `api_keys` |
| 002 | `runs` (trace metadata index) |
| 003 | `agents`, `agent_baselines` |
| 004 | `proposals` |
| 005 | `capability_listings`, `bilateral_agreements`, `transactions` |
| 006 | Auth trigger (`handle_new_user`), `audit_events`, `invitations`, `updated_at` columns, API key scopes, agent policy storage |
| 007 | Row-Level Security policies on all tables |

---

## Local Development

### Prerequisites

- Go 1.23+
- Docker (for local Postgres and MinIO)
- A Supabase project (for auth)

### Setup

```bash
# Start local services
docker-compose up -d

# Run migrations
psql $DATABASE_URL < migrations/001_orgs_users_keys.sql
psql $DATABASE_URL < migrations/002_runs.sql
psql $DATABASE_URL < migrations/003_agents_baselines.sql
psql $DATABASE_URL < migrations/004_proposals.sql
psql $DATABASE_URL < migrations/005_trust_network.sql
psql $DATABASE_URL < migrations/006_auth_sync_audit.sql
psql $DATABASE_URL < migrations/007_rls_policies.sql

# Start the API server
DATABASE_URL="postgres://postgres:postgres@localhost:54322/postgres" \
SUPABASE_JWT_SECRET="your-jwt-secret" \
S3_ENDPOINT="http://localhost:9000" \
S3_BUCKET="traces" \
S3_ACCESS_KEY="minioadmin" \
S3_SECRET_KEY="minioadmin" \
go run ./cmd/relic-api

# Start the governance worker (separate terminal)
DATABASE_URL="postgres://postgres:postgres@localhost:54322/postgres" \
ANTHROPIC_API_KEY="sk-ant-..." \
S3_ENDPOINT="http://localhost:9000" \
S3_BUCKET="traces" \
S3_ACCESS_KEY="minioadmin" \
S3_SECRET_KEY="minioadmin" \
go run ./cmd/relic-governance
```

### Environment Variables

| Variable | Required | Description |
|---|---|---|
| `DATABASE_URL` | Yes | Postgres connection string |
| `SUPABASE_JWT_SECRET` | Yes | JWT secret from Supabase dashboard |
| `PORT` | No | API port (default: 8080) |
| `S3_ENDPOINT` | Yes | S3-compatible endpoint (Cloudflare R2, MinIO) |
| `S3_BUCKET` | Yes | Bucket name for trace storage |
| `S3_ACCESS_KEY` | Yes | S3 access key |
| `S3_SECRET_KEY` | Yes | S3 secret key |
| `S3_REGION` | No | S3 region (default: auto) |
| `ANTHROPIC_API_KEY` | No | Enables LLM-powered intent classification |
| `STRIPE_SECRET_KEY` | No | Enables billing features |

### Deployment

Configured for Fly.io — see `fly.toml` and `docs/DEPLOYMENT.md`.

```bash
fly launch --name therelic-api --region iad
fly secrets set DATABASE_URL="..." SUPABASE_JWT_SECRET="..."
fly deploy
```

---

## Project Structure

```
cmd/
  relic-api/           # HTTP API server entry point
  relic-governance/    # Governance worker entry point
internal/
  api/                 # HTTP handlers, middleware, routing
    middleware/         # Auth, rate limiting
  governance/          # Worker, detector, classifier, proposer
  billing/             # Stripe integration, usage metering
  storage/             # Postgres and S3 clients
migrations/            # SQL migration files (001-007)
docs/                  # Architecture, deployment, development guides
```

---

## Documentation

| Document | Description |
|---|---|
| `docs/ARCHITECTURE.md` | Full technical architecture |
| `docs/BUILD_PLAN.md` | Master build plan and phasing |
| `docs/DEPLOYMENT.md` | Fly.io, Cloudflare, Supabase deployment guide |
| `docs/DEVELOPMENT.md` | Local development setup |
| `docs/DOMAINS.md` | Domain and DNS architecture |
