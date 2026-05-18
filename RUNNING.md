# RUNNING The Relic

Five paths to a working install. Pick one based on what you want and
how much infrastructure you already have. Every path lands on the
same dashboard, with the same data shape, against the same code.

| Path | Best for | Time | What you need |
|---|---|---|---|
| 1. Laptop (Docker) | Try it / personal use | 5 min | Docker Desktop |
| 2. Team self-host (Neon + R2) | Small team, $0-ish ongoing | 30 min | Cloudflare + Neon free accounts |
| 3. Fly.io | One-machine cloud deploy | 30 min | Fly account |
| 4. Supabase | Existing Supabase users | 20 min | Supabase project |
| 5. Full BYO | Enterprise on-prem | a day | Your Postgres + your S3 + your IdP |

The three repos are independent. [therelic-platform](https://github.com/therelicai/therelic-platform)
is the control plane (this repo). [therelic-app](https://github.com/therelicai/therelic-app)
is the dashboard. [therelic](https://github.com/therelicai/therelic) is the
OSS runtime your agents call.

---

## Path 1 · Laptop (5 minutes)

The default. Docker brings up Postgres, MinIO, the API, and the
dashboard, with one admin user.

```bash
git clone https://github.com/therelicai/therelic-platform
cd therelic-platform
./scripts/setup.sh
```

The wizard asks four questions (Postgres / blob / auth / admin) and
writes `.env`. Defaults are oriented at "works on my laptop." Then:

```bash
docker compose up -d
curl http://localhost:8080/readyz
```

The dashboard lives in a sibling repo:

```bash
cd ..
git clone https://github.com/therelicai/therelic-app
cd therelic-app
VITE_AUTH_MODE=local VITE_API_URL=http://localhost:8080/v1 npm install
VITE_AUTH_MODE=local VITE_API_URL=http://localhost:8080/v1 npm run build
npx vite preview
# Open http://localhost:4173, log in with the email + password you set.
```

To push traces from any shell, install the runtime CLI:

```bash
go install github.com/therelicai/therelic/cmd/relic@latest
export RELIC_API_KEY=rk_...   # generated in dashboard Settings → API Keys
relic init && relic trace push
```

---

## Path 2 · Team self-host (Neon + Cloudflare R2)

Free tiers carry a small team a long way. Neon gives you 3 GB Postgres
free; R2 gives you 10 GB blob storage + zero egress.

1. Create a [Neon](https://neon.tech) project. Copy the
   `DATABASE_URL` (pooled, with `?sslmode=require`).
2. Create a [Cloudflare R2](https://developers.cloudflare.com/r2/)
   bucket called `relic-traces`. Note your account ID and generate an
   R2 access key + secret in the dashboard.
3. Set up the platform:

   ```bash
   git clone https://github.com/therelicai/therelic-platform
   cd therelic-platform
   ./scripts/setup.sh
   #   Postgres host kind: managed-supabase-neon-rds-other
   #   DATABASE_URL: <paste from Neon>
   #   S3 kind: cloudflare-r2
   #   R2 account ID: <your account>
   #   bucket: relic-traces
   ```

4. Deploy. Easiest is Fly.io (Path 3) or any Docker-friendly host;
   the simplest for a one-person team is to run `docker compose up -d`
   on a cheap VPS (a $5/mo DigitalOcean droplet handles this stack).

5. Build and host the dashboard. Cloudflare Pages is the free path:

   ```bash
   cd ../therelic-app
   VITE_AUTH_MODE=local \
     VITE_API_URL=https://api.your-domain.com/v1 \
     npm run build
   # Upload dist/ to Cloudflare Pages, or any static host.
   ```

Costs (small team, < 1M traces/mo): $0 - $5/mo.

---

## Path 3 · Fly.io (one-machine cloud)

Fly has a generous free tier and runs the existing `fly.toml`
out-of-the-box.

```bash
git clone https://github.com/therelicai/therelic-platform
cd therelic-platform
flyctl launch  # uses fly.toml
flyctl secrets set \
  RELIC_AUTH_MODE=local \
  RELIC_JWT_SECRET="$(openssl rand -hex 32)" \
  RELIC_ADMIN_EMAIL=you@example.com \
  RELIC_ADMIN_PASSWORD="$(openssl rand -hex 16)" \
  DATABASE_URL="postgres://..."   # Fly Postgres or Neon
flyctl deploy
```

The dashboard goes on Cloudflare Pages or `flyctl deploy` from
[therelic-app](https://github.com/therelicai/therelic-app)'s own
Dockerfile (the SPA serves as static assets behind nginx).

---

## Path 4 · Supabase

If you already use Supabase for auth, this preserves all your existing
identity setup.

1. Create a [Supabase](https://supabase.com) project. Copy:
   - The Postgres connection string from Settings → Database
   - The JWT secret from Settings → API
2. Set up the platform:

   ```bash
   ./scripts/setup.sh
   #   Postgres host kind: managed-supabase-neon-rds-other
   #   DATABASE_URL: <Supabase pooled connection string>
   #   S3 kind: cloudflare-r2 (or local-minio for laptop dev)
   #   Auth mode: supabase
   #   SUPABASE_JWT_SECRET: <paste from Supabase>
   ```

3. New signups in your Supabase project will auto-create Relic orgs
   via the `handle_new_user` trigger (installed when
   `RELIC_AUTH_MODE=supabase`).

4. Build the dashboard with the Supabase backend:

   ```bash
   cd ../therelic-app
   VITE_AUTH_MODE=supabase \
     VITE_API_URL=https://api.your-domain.com/v1 \
     VITE_SUPABASE_URL=https://<project>.supabase.co \
     VITE_SUPABASE_ANON_KEY=<anon-key> \
     npm run build
   ```

---

## Path 5 · Full BYO (enterprise on-prem)

For the deployment where every dependency is yours: your Postgres
cluster, your S3-compatible object store, your identity provider, your
container runtime.

1. Provision Postgres 16+. Apply migrations from `migrations/`. If
   you want defense-in-depth RLS, also apply `migrations.rls/` and set
   `RELIC_RLS_ENABLED=true`.
2. Provision an S3-compatible bucket. Note endpoint, region, access
   key, secret key.
3. Run the `relic-api` container (or `cmd/relic-api` directly) with
   every env var set explicitly. See
   [README.md](./README.md) → Environment Variables.
4. OIDC auth lands in Phase 1 of [ROADMAP.md](../therelic-app/ROADMAP.md);
   for now bridge via Supabase Auth (which speaks OIDC + SAML on paid
   tiers) or use local-auth with SCIM-style API key provisioning.
5. Deploy the dashboard as static assets behind your existing ingress.
   The SPA only needs `VITE_API_URL` to reach the platform.

---

## Wiring your agents to the platform

Once the platform is running, point your agents at it via the OSS
runtime. Three patterns, pick what fits:

- **`relic connect claude-code`** — rewrites `~/.claude.json` MCP
  entries to route through Relic. One command, every MCP server
  governed. Idempotent, fully reversible with `--unwrap`.
- **`relic gateway`** — one stdio MCP server that fans out to N
  upstream servers from `~/.relic/gateway.yaml`. Useful when you
  want one config entry that covers many tools.
- **`relic daemon`** — long-running process: HTTP proxy for
  agent-to-model and agent-to-REST traffic, plus a trace pusher that
  ships finished `.trtrace` files to this platform every 30 seconds.

Full reference: [therelic/docs/CONNECTING.md](../therelic/docs/CONNECTING.md).

---

## Troubleshooting

The dashboard ships a `/health` page (top-right of the sidebar) that
shows whether the platform is reachable, the database is connected,
and blob storage is responding. **Visit it first** when something is
wrong; it's the difference between a five-minute config typo and an
afternoon of log-reading.

Common failures:

| Symptom | Likely cause |
|---|---|
| Dashboard login returns "invalid credentials" | Wrong `RELIC_ADMIN_PASSWORD` in `.env`, or the platform booted before you set it. Re-run `./scripts/setup.sh` to regenerate and `docker compose up -d --force-recreate relic-api`. |
| `/v1/auth/login` returns 404 | You built the dashboard with `VITE_AUTH_MODE=supabase` but the platform is running `RELIC_AUTH_MODE=local` (or vice versa). The login endpoint only exists in local mode. |
| `/readyz` shows S3 fail | Bucket doesn't exist yet (run `docker compose up minio-init` for local MinIO, or create the bucket manually in your cloud provider) or credentials are wrong. |
| CLI says "cannot reach Relic platform at http://localhost:8080" | Platform isn't running, or it's at a different URL. `docker compose up -d` or `export RELIC_API_URL=https://your-host/v1`. |

For anything deeper, the platform writes JSON logs to stdout. `docker
compose logs relic-api` is usually enough.
