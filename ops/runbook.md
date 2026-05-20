# Production runbook — `api.therelic.dev`

Authoritative operations doc for the hosted API. Self-host operators
also benefit from the sections marked **(applies to self-host)**.

## Stack at a glance

| Component | Provider | Where to find it |
|---|---|---|
| DNS + TLS | Cloudflare | `therelic.dev` zone |
| API | Fly.io | app `therelic-api`, config `fly.production.toml` |
| SPA | Cloudflare Pages | project `therelic-app` |
| Postgres | Neon Pro | project `therelic-prod` |
| Blobs | Cloudflare R2 | bucket `relic-traces-prod` |
| Logs | Fly + Cloudflare analytics | `flyctl logs --app=therelic-api` |
| Uptime | UptimeRobot | monitor `relic-api-readyz` |

## Initial setup (one-time)

```bash
# 1. Fly app + machines
flyctl apps create therelic-api --org=therelic
flyctl secrets set --app=therelic-api \
  DATABASE_URL=postgresql://...                                 # Neon Pro connection string
  DATABASE_REPLICA_URL=postgresql://...                         # Neon read endpoint
  RELIC_AUTH_MODE=oidc \
  RELIC_OIDC_DISCOVERY_URL=https://accounts.google.com/.well-known/openid-configuration \
  RELIC_OIDC_CLIENT_ID=...                                       # from Google Cloud Console
  RELIC_OIDC_CLIENT_SECRET=... \
  RELIC_OIDC_REDIRECT_URL=https://api.therelic.dev/v1/auth/oidc/callback \
  RELIC_OIDC_DEFAULT_ORG_ID=...                                  # the shared org id
  RELIC_JWT_SECRET=$(openssl rand -hex 32) \
  RELIC_TRACE_KEY=$(openssl rand -hex 32) \
  RELIC_API_KEY_PEPPER=$(openssl rand -hex 32) \
  RELIC_EVIDENCE_KEY=$(openssl rand -hex 32) \
  RELIC_SECRETS_KEY=$(openssl rand -hex 32) \
  S3_ENDPOINT=https://<account>.r2.cloudflarestorage.com \
  S3_BUCKET=relic-traces-prod \
  S3_ACCESS_KEY=... \
  S3_SECRET_KEY=... \
  S3_REGION=auto \
  ALLOWED_ORIGINS=https://app.therelic.dev

flyctl deploy --config fly.production.toml --remote-only
flyctl ssh console --app=therelic-api -C "/bin/relic-api migrate up"

# 2. Cloudflare DNS
# A     api.therelic.dev  → Fly IPv4 (proxy OFF; flycerts needs direct)
# CNAME app.therelic.dev  → therelic-app.pages.dev  (proxy ON)

# 3. Cloudflare Pages
cd ../therelic-app
npx wrangler pages project create therelic-app
# Build settings in the CF dashboard:
#   Build command: VITE_AUTH_MODE=oidc VITE_API_URL=https://api.therelic.dev/v1 npm run build
#   Output: dist/

# 4. UptimeRobot monitor
# Type: HTTPS, URL: https://api.therelic.dev/readyz, interval: 5 min,
# alert on: status != 200.
```

## Daily deploys

`main` push → GitHub Actions runs `deploy-api.yml` and `deploy-app.yml`.
Both are auto-triggered; no manual step needed for a normal release.

If CI is wedged: `flyctl deploy --config fly.production.toml --remote-only`
from the platform repo, and `wrangler pages deploy dist/` from the app
repo.

## Rollback

```bash
flyctl releases --app=therelic-api               # find the last good release
flyctl deploy --app=therelic-api --image registry.fly.io/therelic-api:v<N>
```

For the app: Cloudflare Pages keeps every deploy; promote a prior one
from the CF dashboard ("Deployments" → "View build" → "Rollback").

## Migrations

```bash
# Always backup first (database only is fast; --include-blobs is slow):
flyctl ssh console --app=therelic-api -C "/bin/relic-api backup /tmp/pre-migrate-$(date +%s).tar.gz"
flyctl ssh sftp get /tmp/pre-migrate-*.tar.gz ./backups/

# Then run the migrate
flyctl ssh console --app=therelic-api -C "/bin/relic-api migrate up"
```

The CI workflow runs `migrate up` after each deploy, so usually you
don't run this manually. The exception: schema changes that need to
land BEFORE the new app version starts serving. In that case, push a
PR that's migration-only first, let it deploy, then push the code
that depends on the new schema.

## Restore from backup

```bash
flyctl ssh sftp put backups/pre-migrate-*.tar.gz /tmp/restore.tar.gz --app=therelic-api
flyctl ssh console --app=therelic-api -C "/bin/relic-api restore /tmp/restore.tar.gz"
```

If the bundle was made with `--include-blobs`, the restore also
pushes blobs back to the configured R2 bucket. If not, mirror your
bucket separately.

## Adding a new env var

```bash
flyctl secrets set --app=therelic-api KEY=value
# Sets the secret AND restarts the machines. There is no separate
# "redeploy" step.
```

For non-secret config (e.g. `RELIC_OTEL_ENDPOINT` for staging) edit
`fly.production.toml` and redeploy.

## Logs

```bash
flyctl logs --app=therelic-api                  # tail
flyctl logs --app=therelic-api | grep <run_id>  # grep by request-id
```

Structured logs use `slog`, JSON format. Common fields:
`request_id`, `route`, `status`, `duration_ms`, `org_id`.

## Common failures + first thing to check

| Symptom | First check |
|---|---|
| `/readyz` returns 503 with `db: fail` | Neon dashboard. If Neon is down, page on-call. Otherwise check `DATABASE_URL` secret. |
| `/readyz` returns 503 with `s3: fail` | R2 credentials. Are `S3_ACCESS_KEY` + `S3_SECRET_KEY` set? Rotate them in the R2 console if needed. |
| OIDC redirect loop | `RELIC_OIDC_REDIRECT_URL` mismatch with the IdP's registered redirect. Both must match exactly (scheme + host + path). |
| API returns 401 for valid JWTs | `RELIC_JWT_SECRET` changed without a coordinated app rebuild. Roll the secret back; sessions issued before the change are still valid against the old secret. |
| Trace downloads fail with 502 | R2 presign rejected. Check `S3_*` secrets and that the bucket policy allows GetObject from the presigner's identity. |
| App can't reach API (CORS) | `ALLOWED_ORIGINS` must include the exact origin (no trailing slash). |

## On-call escalation

1. UptimeRobot pages on `/readyz` 5xx.
2. First responder runs `flyctl logs` + `/readyz` curl to confirm.
3. If Neon: escalate to Neon support via the project dashboard.
4. If Fly: post in the Fly Community Slack first; raise ticket if no
   resolution within 30 min.
5. If R2: Cloudflare support via dashboard.
