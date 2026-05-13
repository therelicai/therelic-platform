# The Relic Platform

**The self-hostable control plane for [The Relic](https://github.com/therelicai/therelic) — trace storage, audit indexing, governance worker, and policy authority.**

This repo runs the server side of The Relic stack. Trace uploads land here, get parsed + verified, persist into Postgres (metadata) + S3 (raw events), and become queryable by the [therelic-app](https://github.com/therelicai/therelic-app) dashboard. The governance worker scans for policy gaps and proposes new rules for human review.

> **License:** Business Source License 1.1 (BSL 1.1). Source-available,
> not OSI-open. Self-host for any purpose — internal production use is
> explicitly permitted by the Additional Use Grant — but you may not
> run a hosted Governance Service that competes with `api.therelic.dev`.
> Each released file converts to Apache License 2.0 four years after
> publication. See [LICENSE](./LICENSE), [NOTICE](./NOTICE), and
> [TRADEMARKS.md](./TRADEMARKS.md).

---

## Quickstart — self-host the whole stack

```bash
# 1. Bring up Postgres, MinIO, run migrations, start relic-api.
git clone https://github.com/therelicai/therelic-platform
cd therelic-platform
docker compose up -d

# 2. (Optional but recommended) generate production secrets and
#    restart so trace HMAC chain verification + peppered API key
#    hashing are active.
cp .env.example .env
echo "RELIC_TRACE_KEY=$(openssl rand -hex 32)"      >> .env
echo "RELIC_API_KEY_PEPPER=$(openssl rand -hex 32)" >> .env
docker compose up -d --force-recreate relic-api

# 3. Sanity check: /readyz returns 200 once Postgres + S3 are
#    reachable.
curl -fsS http://localhost:8080/readyz | jq

# 4. From any shell with the relic CLI installed
#    (https://github.com/therelicai/therelic#install):
relic init
export RELIC_API_URL=http://localhost:8080
export RELIC_API_KEY=rk_dev_test_key_do_not_use_in_production
export RELIC_TRACE_KEY=...   # same value as step 2 if you set one
relic run --mode permissive -- python my_agent.py
relic trace push             # uploads to relic-api
```

### Adding the dashboard

The dashboard ([therelic-app](https://github.com/therelicai/therelic-app))
is shipped as a separate repo and a separate compose overlay. Clone
it next to this one and bring the whole stack up with one command:

```bash
git clone https://github.com/therelicai/therelic-app ../therelic-app
docker compose -f docker-compose.yml -f docker-compose.app.yml up -d --build
open http://localhost:5173
```

The compose stack is sized for a laptop. For production deployments
see [`docs/DEPLOYMENT.md`](./docs/DEPLOYMENT.md).

### Creating a real API key

The dev key seeded by `docker compose up` is fine for localhost but
should never escape your machine. To create a real one, hit the API
with the dev key once:

```bash
curl -s -X POST http://localhost:8080/v1/orgs/00000000-0000-0000-0000-000000000001/api-keys \
  -H "Authorization: Bearer rk_dev_test_key_do_not_use_in_production" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-key"}' | jq
```

The response includes the plaintext key once — store it. If you set
`RELIC_API_KEY_PEPPER`, new keys are stored as `HMAC-SHA256(pepper,
key)`; the legacy SHA-256 plain-hash path stays available for the
seed key until you rotate it.

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
│  This Repo — therelic-platform (BSL 1.1)                     │
│                                                              │
│  relic-api          — HTTP control plane: trace ingest,      │
│                       integrity verification, policy serve   │
│  relic-governance   — Background worker: denial-pattern      │
│                       detection, proposal generation         │
│  Postgres           — orgs, users, runs, agents, proposals   │
│  S3 / MinIO         — raw NDJSON trace events                │
└──────────────────┬───────────────────────────────────────────┘
                   │  REST /v1/*
                   ▼
┌─────────────────────────────────────────────────────────────┐
│  therelic-app — React SPA dashboard (BSL 1.1)                │
│  therelic-website — Marketing site (Apache 2.0)               │
└─────────────────────────────────────────────────────────────┘
```

The runtime and the platform communicate **only over HTTP** — there is no shared Go import. The CLI works entirely standalone with local traces and local policy. The platform is an optional upgrade that adds cloud storage, team collaboration, and governance automation.

---

## Architecture

### Two Services

| Service | Entry Point | Purpose |
|---|---|---|
| **relic-api** | `cmd/relic-api/main.go` | HTTP control plane serving all REST endpoints |
| **relic-governance** | `cmd/relic-governance/main.go` | Background worker that polls for denial patterns and generates proposals |

### API Endpoints

Base: `https://api.therelic.dev/v1` — Auth via Bearer JWT (Supabase Auth) or org-scoped API key.

Full request/response schemas live in [docs/api.md](./docs/api.md). Cross-repo
contracts (selector shape, event types, replay protocol) live in
[RELIC.md](./RELIC.md).

| Group | Endpoints | Description |
|---|---|---|
| **Traces** | `POST /traces`, `GET /traces`, `GET /traces/:id`, `GET /traces/:id/events`, `DELETE /traces/:id` | Upload, list, view, download, and delete trace runs |
| **Policy simulator** | `POST /policy/simulate`, `GET /policy/simulate/:job_id` | Replay candidate policy against historical traces and report the diff. The diff badge in the policy editor is the consumer. |
| **Live feed** | `POST /intents`, `GET /orgs/:orgID/live` | Runtimes push sealed `intent`/`action` events as they happen; dashboards subscribe to the SSE stream. Postgres LISTEN/NOTIFY backplane, no extra infra. |
| **Universal policy** | `POST /policy_sets`, `PUT /policy_sets/:id`, `GET /policy_sets/:id`, `POST /policy_sets/resolve`, `POST /agents/:name/labels`, `POST /agents/:name/policy_applied`, `GET /agents/:name/policy_updates` (SSE, agent-facing) | One policy_set applies to a labeled agent set. The agent-facing SSE channel is distinct from the dashboard one — different auth (API key vs JWT), different audience, different event shape. |
| **Agents** | `POST /agents`, `GET /agents`, `GET /agents/:name`, `GET /agents/:name/policy`, `PUT /agents/:name/policy`, `GET /agents/:name/baseline` | Agent registry, policy distribution, behavioral baselines |
| **Organizations** | `POST /orgs`, `GET /orgs/:id`, `POST /orgs/:id/api-keys`, `DELETE /orgs/:id/api-keys/:kid` | Org management and API key lifecycle |
| **Proposals** | `GET /proposals`, `GET /proposals/:id`, `POST /proposals/:id/approve`, `POST /proposals/:id/reject`, `DELETE /proposals/:id` | Governance proposals from automated detection |
| **Audit** | `GET /audit-events` | Platform-level audit trail |
| **Onboarding** | `POST /onboard` | Explicit org creation for new signups |
| **Registry** | `GET /registry`, `POST /registry`, `PUT /registry/:agentID`, `GET /registry/:agentID/trust` | Trust network and marketplace (future) |
| **Transactions** | `GET /transactions`, `GET /transactions/summary` | Metered agent-to-agent transactions (future) |

### Middleware

- **CORS** — Configurable via `ALLOWED_ORIGINS` (comma-separated)
- **Rate limiting** — Token bucket per IP (10 req/s, burst 20)
- **Auth** — JWT validation (HS256, configurable issuer/audience) or API key HMAC-SHA256 lookup with server pepper
- **Request logging** — Structured JSON via `slog`, includes `request_id` (also returned via `X-Request-ID`)
- **Metrics** — `/metrics` (Prometheus) and `/readyz` (DB + S3 health)

### Governance Engine

The governance worker runs on a 60-second poll interval:

1. **Detection** — For each org, scans recent runs with denials. Downloads gzipped NDJSON traces from S3 to identify which specific tools were denied (not just aggregate counts).
2. **Classification** — Sends denied tool + parameters to Claude for intent classification: is this a policy gap (user wants it allowed) or a correct denial (intentionally blocked)?
3. **Proposal generation** — For classified gaps, creates a governance proposal with the suggested policy rule, evidence (run IDs, denial counts), and the LLM's reasoning.

Proposals appear in the dashboard for human review (approve/reject/dismiss).

### Database Schema

Migrations in `migrations/` are applied in order by the `migrate`
compose service. A `schema_migrations` tracking table is used so
re-running `docker compose up` is idempotent.

| Migration | Tables / change |
|---|---|
| 001 | `organizations`, `users`, `api_keys` |
| 002 | `runs` (trace metadata index) |
| 003 | `agents`, `agent_baselines` |
| 004 | `proposals` |
| 005 | `capability_listings`, `bilateral_agreements`, `transactions` |
| 006 | Auth trigger (`handle_new_user`), `audit_events`, `invitations`, `updated_at` columns, API key scopes, agent policy storage |
| 007 | Row-Level Security policies on all tables |
| 008 | Run integrity columns (`integrity_chain`, `signed_envelope`) |
| 009 | RLS completeness (`FORCE ROW LEVEL SECURITY` on all tenant tables) |
| 010 | API key hash algorithm column (HMAC-SHA256 with pepper) |
| 011 | `runs.chain_verified` for server-validated HMAC chain |

---

## Local Development

### Prerequisites

- Docker (and `docker compose`) — for the integrated stack
- Go 1.23+ — only if you want to run the API directly without Docker

### Run everything in Docker (recommended)

```bash
docker compose up -d
docker compose logs -f relic-api    # follow API logs
curl http://localhost:8080/readyz   # 200 once Postgres + MinIO are up
```

This brings up Postgres (`:54322`), MinIO (`:9000`, console `:9001`),
applies migrations once, creates the trace bucket, and starts
`relic-api` on `:8080`. Re-running the same command is safe — the
`schema_migrations` table tracks what's already been applied.

### Run the API outside Docker

```bash
docker compose up -d postgres minio minio-init migrate

DATABASE_URL="postgres://relic:relic@localhost:54322/therelic?sslmode=disable" \
S3_ENDPOINT="http://localhost:9000" \
S3_BUCKET="relic-traces" \
S3_ACCESS_KEY="relicminio" \
S3_SECRET_KEY="relicminio" \
S3_REGION="us-east-1" \
ALLOWED_ORIGINS="http://localhost:5173,http://localhost:5174" \
go run ./cmd/relic-api
```

The governance worker is a separate binary:

```bash
DATABASE_URL="postgres://relic:relic@localhost:54322/therelic?sslmode=disable" \
ANTHROPIC_API_KEY="sk-ant-..." \
S3_ENDPOINT="http://localhost:9000" \
S3_BUCKET="relic-traces" \
S3_ACCESS_KEY="relicminio" \
S3_SECRET_KEY="relicminio" \
go run ./cmd/relic-governance
```

### Environment Variables

#### Required

| Variable | Description |
|---|---|
| `DATABASE_URL` | Postgres connection string |
| `S3_ENDPOINT` | S3-compatible endpoint (MinIO, Cloudflare R2, AWS) |
| `S3_BUCKET` | Bucket name for trace storage |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | S3 credentials |

#### Strongly recommended for production

| Variable | Description |
|---|---|
| `SUPABASE_JWT_SECRET` | JWT secret for HS256 dashboard auth |
| `RELIC_JWT_ISSUER` / `RELIC_JWT_AUDIENCE` | Expected JWT `iss` / `aud` claims |
| `RELIC_API_KEY_PEPPER` | Server-side pepper for HMAC-SHA256 API key hashing (Slice 3) |
| `RELIC_TRACE_KEY` | 32-byte hex master secret enabling server-side HMAC chain verification (Slice 6) |
| `RELIC_REQUIRE_SEALED_TRACES` | `1` to reject uploads missing an HMAC chain |
| `ALLOWED_ORIGINS` | Comma-separated CORS origin allowlist |

#### Operational tuning

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | API port |
| `S3_REGION` | `auto` | S3 region |
| `RELIC_PG_MAX_CONNS` | `20` | pgxpool max connections |
| `RELIC_PG_MIN_CONNS` | `2` | pgxpool min connections |
| `RELIC_PG_MAX_LIFETIME` | `30m` | Connection lifetime |
| `RELIC_PG_MAX_IDLE_TIME` | `5m` | Idle connection timeout |
| `RELIC_RETENTION_DISABLED` | unset | Set to `1` to skip the retention sweeper |
| `RELIC_RETENTION_INTERVAL` | `15m` | Retention worker sweep interval |
| `RELIC_RETENTION_BATCH` | `100` | Max runs reaped per sweep |
| `ANTHROPIC_API_KEY` | — | Enables LLM-powered intent classification (governance worker) |

### Observability

| Endpoint | Purpose |
|---|---|
| `GET /readyz` | Returns 200 once Postgres + S3 are reachable; 503 otherwise |
| `GET /metrics` | Prometheus exposition: HTTP, trace uploads, retention sweeps, DB pool stats |

### Deployment

For Fly.io see `fly.toml` and `docs/DEPLOYMENT.md`. For any other
container host, the included `Dockerfile` is single-stage-Alpine and
~30 MB — drop it on Render, Railway, Cloud Run, ECS, or your own
Kubernetes cluster.

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
  metrics/             # Prometheus instrumentation
  retention/           # Background sweeper for expired traces
  storage/             # Postgres and S3 clients
  trace/               # Server-side NDJSON parser + HMAC verifier
migrations/            # SQL migration files
docs/                  # Architecture, deployment, development guides
```

---

## Documentation

| Document | Description |
|---|---|
| [RELIC.md](./RELIC.md) | Cross-repo alignment doc — selector contract, event shapes, replay protocol, lifecycle. Authoritative across all four repos. |
| [docs/api.md](./docs/api.md) | Full endpoint reference (request/response schemas). The README endpoint table summarizes; this is the source of truth. |
| [CHANGELOG.md](./CHANGELOG.md) | Release notes (Keep-a-Changelog format). |
| [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) | Full technical architecture |
| [docs/BUILD_PLAN.md](./docs/BUILD_PLAN.md) | Master build plan and phasing |
| [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md) | Fly.io, Cloudflare, Supabase deployment guide |
| [docs/DEVELOPMENT.md](./docs/DEVELOPMENT.md) | Local development setup |
| [docs/DOMAINS.md](./docs/DOMAINS.md) | Domain and DNS architecture |
