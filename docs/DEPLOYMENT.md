# The Relic Platform — Deployment Guide

This document covers deploying the control plane API, web app, and supporting infrastructure.

---

## 1. Go API on Fly.io

### Prerequisites

- [Fly CLI](https://fly.io/docs/hands-on/install-flyctl/) installed
- Fly.io account (`fly auth signup` or `fly auth login`)

### Initial Setup

From the `therelic-platform` directory:

```bash
# Launch the app (creates fly.toml if not present)
fly launch --name therelic-api --no-deploy

# Or if fly.toml already exists:
fly apps create therelic-api
```

### Configure Secrets

Set all required environment variables as Fly secrets (never commit these):

```bash
fly secrets set DATABASE_URL="postgres://user:pass@host:5432/therelic?sslmode=require"
fly secrets set SUPABASE_JWT_SECRET="your-supabase-jwt-secret"
fly secrets set S3_ENDPOINT="https://your-account-id.r2.cloudflarestorage.com"
fly secrets set S3_BUCKET="traces"
fly secrets set S3_ACCESS_KEY="your-r2-access-key"
fly secrets set S3_SECRET_KEY="your-r2-secret-key"
fly secrets set S3_REGION="auto"
fly secrets set STRIPE_SECRET_KEY="sk_live_..."
fly secrets set ANTHROPIC_API_KEY="sk-ant-..."
```

### Deploy

```bash
fly deploy
```

### Useful Commands

```bash
fly status              # Check app status
fly logs                # View logs
fly ssh console         # SSH into the machine
fly scale count 1       # Ensure 1 machine
fly secrets list        # List configured secrets
```

---

## 2. Environment Variables Reference

| Variable | Required | Description |
|----------|----------|-------------|
| `DATABASE_URL` | Yes | Postgres connection string (Supabase or other) |
| `SUPABASE_JWT_SECRET` | Yes | JWT secret from Supabase Dashboard → Settings → API |
| `S3_ENDPOINT` | Yes | S3/R2 endpoint URL (e.g. Cloudflare R2) |
| `S3_BUCKET` | Yes | Bucket name for trace storage |
| `S3_ACCESS_KEY` | Yes | S3/R2 access key |
| `S3_SECRET_KEY` | Yes | S3/R2 secret key |
| `S3_REGION` | Yes | Region (use `auto` for R2) |
| `STRIPE_SECRET_KEY` | Yes | Stripe secret key for billing |
| `ANTHROPIC_API_KEY` | Yes | Anthropic API key for governance agent LLM |
| `PORT` | No | HTTP port (default: 8080) |

---

## 3. React App on Cloudflare Pages

### Build

From the `therelic-app` directory:

```bash
npm run build
```

The output is in `dist/`.

### Deploy to Cloudflare Pages

**Option A: Via Wrangler CLI**

```bash
npx wrangler pages deploy dist --project-name therelic-app
```

**Option B: Via Git integration**

1. Connect your repo to Cloudflare Pages
2. Set build command: `npm run build`
3. Set output directory: `dist`
4. Set root directory: `therelic-app` (if monorepo)
5. Add environment variables:
   - `VITE_API_URL` = `https://api.therelic.dev`
   - `VITE_SUPABASE_URL` = your Supabase project URL
   - `VITE_SUPABASE_ANON_KEY` = your Supabase anon key

**Option C: Via Vite preview + manual upload**

```bash
npm run build
# Upload dist/ to any static host (Netlify, Vercel, etc.)
```

---

## 4. DNS Configuration

Configure these records for `therelic.dev`:

| Type | Name | Value | Purpose |
|------|------|-------|---------|
| A / CNAME | `@` | Cloudflare Pages / your host | Root domain |
| CNAME | `app` | Cloudflare Pages / your host | Web app (app.therelic.dev) |
| CNAME | `api` | `therelic-api.fly.dev` | API (api.therelic.dev) |

For Fly.io, add a custom domain:

```bash
fly certs add api.therelic.dev
```

Then add the CNAME record: `api` → `therelic-api.fly.dev`.

---

## 5. Supabase Setup

### Create Project

1. Create a new project at [supabase.com](https://supabase.com)
2. Note the project URL and anon key for the React app
3. Get the JWT secret: Settings → API → JWT Secret

### Run Migrations

Apply migrations in order:

```bash
# Using Supabase CLI (recommended)
supabase db push

# Or manually via psql
psql "$DATABASE_URL" -f migrations/001_orgs_users_apikeys.sql
psql "$DATABASE_URL" -f migrations/002_runs.sql
psql "$DATABASE_URL" -f migrations/003_agents_baselines.sql
psql "$DATABASE_URL" -f migrations/004_proposals.sql
psql "$DATABASE_URL" -f migrations/005_trust_network.sql
psql "$DATABASE_URL" -f migrations/006_auth_sync_audit.sql
psql "$DATABASE_URL" -f migrations/007_rls_policies.sql
```

### Configure Auth

1. **Authentication → Providers**: Enable Email (and optionally OAuth providers)
2. **Authentication → URL Configuration**:
   - Site URL: `https://app.therelic.dev`
   - Redirect URLs: `https://app.therelic.dev/**`, `http://localhost:5173/**`
3. **Authentication → JWT expiry**: Adjust if needed (default 3600)

### Database URL for API

Use the connection string from Supabase Dashboard → Settings → Database:

- **Direct connection** (for server-side): `postgres://postgres.[ref]:[password]@aws-0-[region].pooler.supabase.com:6543/postgres`
- Use the "Connection pooling" URI for the API (port 6543)

---

## 6. Governance Worker (Optional)

The governance worker can run as a separate Fly app or alongside the API. To deploy as its own app:

1. Create a new `fly.toml` for the governance service (or use `fly launch` with a different app name)
2. Set the same secrets as the API
3. Override CMD in fly.toml to run `/bin/relic-governance` instead of `/bin/relic-api`

Alternatively, run it as a separate process on the same machine using a custom Dockerfile or Procfile.
