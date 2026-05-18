# Deployment

The deep reference for every env var, every auth mode, every storage
option, and the platform CLI subcommands. For the five quickstart
paths (laptop, team self-host, Fly, Supabase, BYO), see
[RUNNING.md](../RUNNING.md).

This doc has four parts:

1. Environment variables, grouped by concern
2. Choosing an auth mode
3. Choosing blob storage
4. Operational tasks (migrate, backup, restore, telemetry)

---

## 1. Environment variables

### Required for every deployment

| Variable | What it is |
|---|---|
| `DATABASE_URL` | Postgres connection string. Format: `postgres://user:pass@host:port/db?sslmode=require`. Use `sslmode=disable` only for local Docker. |
| `RELIC_AUTH_MODE` | `local`, `supabase`, or `oidc`. Determines which adapter validates JWTs and whether the auth.users trigger gets installed. |

### Auth-mode-specific

Set the variables for the mode you picked, leave the others unset.

| `RELIC_AUTH_MODE=local` | Description |
|---|---|
| `RELIC_JWT_SECRET` | 32-byte hex secret signing user JWTs. Generate with `openssl rand -hex 32`. **Rotating this invalidates every active session.** |
| `RELIC_ADMIN_EMAIL` | Optional. On first boot, when no local-auth user exists, the platform creates this email as the admin. |
| `RELIC_ADMIN_PASSWORD` | Optional. Required if `RELIC_ADMIN_EMAIL` is set. ≥ 8 chars. Remove from `.env` after first boot. |
| `RELIC_JWT_TTL` | Optional. Token lifetime as a Go duration (e.g. `24h`, `168h`). Default: 24h. |

| `RELIC_AUTH_MODE=supabase` | Description |
|---|---|
| `SUPABASE_JWT_SECRET` | The HS256 secret from your Supabase project's Settings → API. The platform verifies every JWT against this. |

| `RELIC_AUTH_MODE=oidc` | Description |
|---|---|
| _(stub today)_ | Lands in ROADMAP Phase 1 (SSO/SAML/SCIM). Bootstraps from OIDC discovery + JWKS. |

### Optional but strongly recommended

| Variable | What it does |
|---|---|
| `RELIC_JWT_ISSUER` | Expected `iss` claim. When set, tokens missing or mismatching this are rejected. Pin to your IdP's issuer URL. |
| `RELIC_JWT_AUDIENCE` | Expected `aud` claim. Same idea. |
| `RELIC_API_KEY_PEPPER` | 32-byte hex. Server-side pepper for HMAC-SHA256 API key hashing. Blank means legacy plain SHA-256 (only for in-place rollout). |
| `RELIC_TRACE_KEY` | 32-byte hex. Master secret for HMAC chain verification of uploaded traces. Blank means presence-only mode (badge but no proof). |
| `RELIC_REQUIRE_SEALED_TRACES` | `1` to reject uploads without an HMAC chain. Pair with `RELIC_TRACE_KEY` for full enforcement. |
| `RELIC_RLS_ENABLED` | `true` to apply `migrations.rls/` (defense-in-depth row-level security). The API filters by org at the app layer regardless; this is the second line. |
| `ALLOWED_ORIGINS` | Comma-separated CORS allowlist. Default covers production + local dev. Override for split-origin deploys. |

### Storage

| Variable | Notes |
|---|---|
| `S3_ENDPOINT` | Custom S3 endpoint. Leave blank for AWS S3. R2: `https://<account>.r2.cloudflarestorage.com`. MinIO: `http://minio:9000`. |
| `S3_BUCKET` | Bucket name. Must already exist. The MinIO compose service auto-creates `relic-traces`. |
| `S3_REGION` | Region (or `auto` for R2). |
| `S3_ACCESS_KEY` / `S3_SECRET_KEY` | Credentials. The pair is provider-specific (R2 token, AWS IAM key, MinIO root creds). |

### Operational tuning

| Variable | Default | What it does |
|---|---|---|
| `PORT` | `8080` | API listen port |
| `RELIC_PG_MAX_CONNS` | `20` | pgxpool max connections |
| `RELIC_PG_MIN_CONNS` | `2` | pgxpool min connections |
| `RELIC_PG_MAX_LIFETIME` | `30m` | Connection lifetime cap |
| `RELIC_PG_MAX_IDLE_TIME` | `5m` | Idle connection timeout |
| `RELIC_GOVERNANCE_ENABLED` | unset | When `true`, the governance worker scans for proposals. |
| `RELIC_RETENTION_DISABLED` | unset | When `true`, disables the retention worker. Use for short-lived CI deploys. |
| `RELIC_RETENTION_INTERVAL` | `15m` | Retention sweep interval |
| `RELIC_TELEMETRY` | unset | When `true`, sends opt-in anonymous pings. See §4. |
| `RELIC_TELEMETRY_URL` | `https://telemetry.therelic.dev/ping` | Override if you mirror or self-collect. |
| `ANTHROPIC_API_KEY` | unset | Optional. Enables the governance classifier; without it, proposals fall back to threshold heuristics. |
| `RELIC_MIGRATIONS_DIR` | `/migrations` | Override for `relic-api migrate up` when the SQL lives elsewhere. |
| `RELIC_MIGRATIONS_SUPABASE_DIR` | `/migrations.supabase` | Same, for the Supabase-only folder. |
| `RELIC_MIGRATIONS_RLS_DIR` | `/migrations.rls` | Same, for the RLS folder. |

---

## 2. Choosing an auth mode

| Mode | Use it when | Don't use it when |
|---|---|---|
| `local` | Single-tenant deploys, laptop dev, small teams who don't want a separate IdP | You need SSO, SCIM, or multi-IdP federation |
| `supabase` | You already use Supabase for auth in other apps | You don't want a hosted IdP dependency |
| `oidc` | You have Okta / Entra ID / Authentik / Keycloak. _(Stubbed in v1.)_ | Today (lands in ROADMAP Phase 1) |

Switching modes is reversible. The schema records each user's
`auth_provider`, so a Supabase user stays a Supabase user even after
you flip the platform to local. You can run mixed-mode (rare) by
issuing tokens signed with different secrets and configuring the
middleware to accept either.

**Bootstrap flow for `local`:**

1. Set `RELIC_AUTH_MODE=local`, `RELIC_JWT_SECRET`, `RELIC_ADMIN_EMAIL`, `RELIC_ADMIN_PASSWORD`.
2. First boot: the platform creates the org + admin user from those env vars. Logs `"bootstrapped local admin"`.
3. Remove `RELIC_ADMIN_PASSWORD` from `.env`. Re-deploys are now a no-op for bootstrap.
4. To reset a forgotten password (v1, before SMTP):
   `docker compose exec -T postgres psql -U relic -d therelic -c "UPDATE users SET password_hash = '...new bcrypt hash...' WHERE email = 'admin@yourco';"`

**Bootstrap flow for `supabase`:**

1. Create a Supabase project. Set `SUPABASE_JWT_SECRET` from Settings → API.
2. Set `RELIC_AUTH_MODE=supabase`. The migrate service installs the `auth.users` trigger.
3. Signups to your Supabase project now auto-create Relic orgs + users via the trigger.

---

## 3. Choosing blob storage

The platform speaks pure S3 API with path-style addressing. Any of
these work:

| Provider | Cost | Get-started time |
|---|---|---|
| **MinIO** (Docker) | $0 (your disk) | 0 — wired into compose |
| **Cloudflare R2** | 10 GB + zero egress free, then $0.015/GB | 10 min |
| **Backblaze B2** | 10 GB + 1 GB/day egress free | 10 min |
| **AWS S3** | 5 GB / 12 mo, then standard rates | 15 min |
| **Wasabi / Linode Object** | per provider | varies |

Pick MinIO for laptop, R2 for production self-host (no egress fees is
huge for read-heavy workloads), AWS S3 if you're already on AWS.

Cross-provider migration is `mc mirror` (MinIO Client) or `rclone
sync` between buckets, plus updating `.env`. Trace metadata lives in
Postgres and is unaware of the bucket.

---

## 4. Operational tasks

### Migrations

```bash
# Inspect what's applied
docker compose exec relic-api /bin/relic-api migrate status

# Apply anything pending. Idempotent.
docker compose exec relic-api /bin/relic-api migrate up
```

Docker-compose's `migrate` one-shot service runs the same logic on
every `up`, so for normal flows you don't need to call this directly.
Use the subcommand when you're deploying outside Docker (e.g. Fly,
Kubernetes) or running migrations against a managed Postgres before
the API starts.

### Backup / restore

```bash
# Take a backup. Bundles pg_dump + S3 key list + manifest.
docker compose exec relic-api /bin/relic-api backup /tmp/backup.tar.gz
docker compose cp $(docker compose ps -q relic-api):/tmp/backup.tar.gz ./backup-$(date +%Y%m%d).tar.gz

# Restore (against an empty database)
docker compose exec relic-api /bin/relic-api restore /tmp/backup.tar.gz
```

**S3 objects are not bundled** — bucket sync is your responsibility.
The bundle records every key so you can audit what's missing from a
destination bucket. For production, mirror your bucket via `rclone
sync` on the same schedule as `relic-api backup`.

### Telemetry

Off by default. Set `RELIC_TELEMETRY=true` to send one ping at boot
plus one per 24 hours to `https://telemetry.therelic.dev/ping`.

Payload (the entirety):

```json
{
  "build":              "v0.3.0",
  "commit":             "abc1234",
  "auth_mode":          "local",
  "users_bucket":       "1-10",
  "agents_bucket":      "11-100",
  "runs_bucket":        "101-1000",
  "governance_enabled": false
}
```

No email addresses, no organization names, no agent or run IDs, no
trace contents. Counts are bucketed.

To self-host telemetry collection, set `RELIC_TELEMETRY_URL` to your
own endpoint. The payload shape is stable; we'll version it before
breaking it.

### Health checks

| Endpoint | Auth | Purpose |
|---|---|---|
| `GET /health` | none | Process is up. Cheap; use for liveness probes. |
| `GET /readyz` | none | DB + S3 reachable. Use for readiness probes. |
| `GET /v1/version` | none | Build, commit, schema_version, auth_mode. For client-side skew detection. |
| `GET /metrics` | none (firewall it) | Prometheus exposition. |

The dashboard's `/health` page calls `/readyz` and renders the result.
That's the operator's first stop when something looks wrong.

### Logs

The platform writes JSON to stdout. Every request line includes
`request_id` (also echoed in the `X-Request-ID` response header) so
client-side error reports can be grepped.

```bash
docker compose logs -f relic-api
docker compose logs relic-api | jq 'select(.level == "ERROR")'
```

### Common failure modes

| Symptom | Likely cause |
|---|---|
| `/v1/auth/login` returns 404 | Platform is in supabase or oidc mode; login is local-only. Check `GET /v1/version` for actual mode. |
| First boot doesn't create admin | Either `RELIC_ADMIN_EMAIL`/`PASSWORD` are unset, or a local-auth user already exists. Check `docker compose logs relic-api | grep bootstrap`. |
| Migrations fail on a fresh DB | Wrong `RELIC_AUTH_MODE` for the migrations being applied — `migrations.supabase/` expects `auth.users`. The migrate runner creates the stub when `RELIC_AUTH_MODE=supabase`. |
| Trace upload returns 415 | Body isn't gzipped, or `Content-Type: application/gzip` is missing. The OSS CLI sets both. |
| `/readyz` shows db.status=fail | Wrong `DATABASE_URL`, or the DB is down. Connection details get scrubbed in error messages — check the platform logs for the real failure. |
| `/readyz` shows s3.status=fail | Wrong bucket, wrong credentials, or bucket missing. For local Docker: `docker compose up minio-init`. |
