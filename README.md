# The Relic Platform

**The server side of [The Relic](https://github.com/therelicai/therelic).**
It receives trace uploads from agents, verifies their HMAC chain,
stores them in Postgres + S3, runs a governance worker that proposes
policy improvements, and serves the REST API the dashboard reads.

You don't need to run this to use The Relic — the OSS runtime is fully
standalone. Run the platform when you want a team dashboard, hosted
audit log, or governance automation.

---

## Get started in 5 minutes

Requires Docker (Desktop or any compatible runtime).

```bash
# 1. Clone and configure
git clone https://github.com/therelicai/therelic-platform
cd therelic-platform
./scripts/setup.sh           # interactive: 4 questions, writes .env

# 2. Boot the stack
docker compose up -d
curl -fsS http://localhost:8080/readyz   # 200 once Postgres + S3 are up

# 3. Add the dashboard (optional, but recommended)
git clone https://github.com/therelicai/therelic-app ../therelic-app
( cd ../therelic-app && VITE_AUTH_MODE=local VITE_API_URL=http://localhost:8080/v1 npm install && npm run build && npx vite preview )
open http://localhost:4173
```

Log in with the `RELIC_ADMIN_EMAIL` / `RELIC_ADMIN_PASSWORD` you set
during `setup.sh`. The dashboard shows your first traces as soon as
an agent pushes them.

---

## Where can I run this?

Anywhere with a container runtime, a Postgres, and an S3-compatible
bucket. The wizard handles every common combination:

| Deployment | What you bring | Time |
|---|---|---|
| Laptop (defaults) | Just Docker | 5 min |
| Team self-host | Neon + Cloudflare R2 | 30 min |
| Fly.io | Fly account | 30 min |
| Supabase | Supabase project | 20 min |
| Enterprise on-prem | Your own Postgres + S3 + IdP | A day |

Five deployment paths, end to end, are in
[RUNNING.md](./RUNNING.md). Operational ops (env-var reference,
upgrade procedure, backup/restore) are in
[docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md) and
[docs/UPGRADING.md](./docs/UPGRADING.md).

---

## What's inside

| Service | Entry point | What it does |
|---|---|---|
| `relic-api` | `cmd/relic-api/main.go` | HTTP control plane. Trace ingest, policy serve, auth, governance proposals, audit log. Default port `:8080`. |
| `relic-governance` | `cmd/relic-governance/main.go` | Background worker. Scans denied actions, classifies them (heuristic or via Anthropic if `ANTHROPIC_API_KEY` is set), proposes policy rules. |

Plus subcommands on the same binary for operators:

```bash
relic-api migrate up          # apply pending migrations
relic-api migrate status      # list applied migrations + schema version
relic-api version             # print build + schema version
relic-api backup OUT.tar.gz   # dump database + S3 manifest
relic-api restore IN.tar.gz   # restore from a backup
relic-api reset-password EMAIL [NEW_PASSWORD]   # local-auth recovery
```

---

## How auth works

Three modes, picked at boot via `RELIC_AUTH_MODE`:

| Mode | What it is | When to pick it |
|---|---|---|
| `local` | HS256 JWT signed by `RELIC_JWT_SECRET`. bcrypt passwords in `users.password_hash`. First-boot admin from env vars. | Self-host, small teams, evaluation. |
| `supabase` | HS256 JWT verified against `SUPABASE_JWT_SECRET`. All lifecycle is Supabase's. | Existing Supabase users. |
| `oidc` | Stub today; lands in ROADMAP Phase 1 (SSO/SAML/SCIM). | Enterprise SSO. |

Login (`/v1/auth/login`, local mode only) is rate-limited per IP: 5
attempts burst, then 1 per 10 seconds. Brute force is intentionally
slow.

---

## How storage works

Pure S3 API with path-style addressing. Drop in any S3-compatible
store:

| Provider | Free tier | Notes |
|---|---|---|
| MinIO (Docker) | unlimited (your disk) | Default in `docker compose`. |
| Cloudflare R2 | 10 GB + zero egress | Recommended for production self-host. |
| Backblaze B2 | 10 GB + 1 GB/day | Cheapest pay-as-you-go beyond free. |
| AWS S3 | 5 GB for 12 mo | Standard choice on AWS. |
| Wasabi / Linode | varies | Both work without code changes. |

Switch providers by editing `.env` — no code changes.

---

## The four repos

| Repo | What it is |
|---|---|
| [therelic](https://github.com/therelicai/therelic) | The OSS runtime. CLI + MCP proxy + policy engine. |
| **therelic-platform** (this repo) | Server side. Trace storage, governance, the REST API. |
| [therelic-app](https://github.com/therelicai/therelic-app) | React dashboard that consumes this API. |
| [therelic-website](https://github.com/therelicai/therelic-website) | Marketing site at [therelic.dev](https://therelic.dev). |

All four are Apache 2.0.

---

## Docs

- [RUNNING.md](./RUNNING.md) — five-path quickstart
- [docs/DEPLOYMENT.md](./docs/DEPLOYMENT.md) — env-var reference,
  auth modes, storage providers, operational tasks
- [docs/UPGRADING.md](./docs/UPGRADING.md) — migration table, backup,
  breaking changes, rollback
- [docs/THREAT_MODEL.md](./docs/THREAT_MODEL.md) — assets, trust
  boundaries, defended adversaries, known gaps
- [docs/ARCHITECTURE.md](./docs/ARCHITECTURE.md) — full design
- [docs/api.md](./docs/api.md) — endpoint reference

---

## License

[Apache License 2.0](LICENSE). Trademarks reserved — see
[TRADEMARKS.md](TRADEMARKS.md).
