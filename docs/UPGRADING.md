# Upgrading

The Relic Platform follows semantic versioning at the wire surface
(API + JWT claim shape). Schema migrations are append-only and
idempotent. Self-hosters upgrade by pulling the new image and running
one command.

## TL;DR

```bash
# 1. Back up first.
docker compose exec relic-api /bin/relic-api backup /tmp/relic.tar.gz
docker compose cp $(docker compose ps -q relic-api):/tmp/relic.tar.gz ./backup.tar.gz

# 2. Pull the new image and restart.
docker compose pull relic-api
docker compose up -d --force-recreate relic-api

# 3. Apply pending migrations.
docker compose exec relic-api /bin/relic-api migrate up

# 4. Verify.
docker compose exec relic-api /bin/relic-api version
curl http://localhost:8080/v1/version
```

The `migrate up` step is idempotent. If everything was already applied
by docker-compose's own `migrate` service (which runs on every `up`),
this is a no-op.

## Version compatibility

The platform exposes `GET /v1/version` so clients can detect skew.
The OSS CLI and the dashboard check it on startup.

| Component | Reads `schema_version` from | Behavior on mismatch |
|---|---|---|
| `therelic` CLI | `GET /v1/version` (best effort) | Warns when local CLI was built for an older schema. Doesn't refuse to push. |
| `therelic-app` dashboard | `GET /v1/version` | Shows a banner on the `/health` page when build versions don't line up. |
| `relic-governance` worker | platform DB directly | Refuses to start when the schema is older than the version it was built for. |

Within a minor version (0.X.*), every client speaks every platform.
Major version bumps (X.0.0) document any breaking changes here.

## Backup before upgrading

Every upgrade should be preceded by `relic-api backup`. The bundle is
a tarball:

```
relic-backup/
  manifest.json      schema_version, taken_at, blob_count
  database.sql.gz    pg_dump --no-owner --no-acl, gzipped
  blobs.txt          list of S3 object keys at backup time
```

**S3 blobs are not bundled** — they're too large for routine backups.
Mirror your bucket separately. The blob list is recorded so an
operator restoring to a clean S3 bucket knows which objects are
missing.

Restore:

```bash
docker compose exec relic-api /bin/relic-api restore /tmp/relic.tar.gz
```

The restore replays the pg_dump. It does **not** touch S3.

## Migration version table

Filenames in `migrations/` are append-only and never renumbered.
Gaps in the sequence (e.g. 007 + 009 moved to `migrations.rls/`) are
intentional and preserved so existing installs don't re-apply.

| File | Adds | Repo version it shipped in |
|---|---|---|
| 001 — orgs/users/api_keys | core multi-tenancy | 0.1 |
| 002 — runs | trace metadata | 0.1 |
| 003 — agents/baselines | agent registration | 0.1 |
| 004 — proposals | governance proposals | 0.1 |
| 005 — trust network | listings, agreements, transactions | 0.1 |
| 006 — audit + invitations | audit log, updated_at, scopes | 0.2 (split from old 006_auth_sync_audit) |
| 008 — runs integrity | HMAC chain columns | 0.2 |
| 010 — api key hash algo | pepper + HMAC switch | 0.2 |
| 011 — runs chain_verified | server-verified flag | 0.2 |
| 012 — policy sets + labels | universal policy | 0.2 |
| 013 — local auth | password_hash + auth_provider | 0.3 |
| migrations.supabase/001 | auth.users trigger | 0.3 (was in old 006) |
| migrations.rls/001 + 002 | RLS policies + completeness | 0.3 (was old 007 + 009) |

## Breaking changes

### 0.3 (Self-host release)

Two changes worth knowing about; neither requires action for typical
self-hosters because the defaults preserve old behavior.

- **`RELIC_AUTH_MODE` is now required.** Old installs that read
  `SUPABASE_JWT_SECRET` should set `RELIC_AUTH_MODE=supabase`.
  Docker-compose's `.env.example` defaults to `supabase` for
  backward compatibility.
- **Migration 006 split.** The Supabase `auth.users` trigger moved to
  `migrations.supabase/001_supabase_user_sync.sql`. Old installs
  already have the trigger; the new migration is idempotent (uses
  `CREATE OR REPLACE` + `DROP TRIGGER IF EXISTS`), so re-applying
  is safe.

## Rolling back

The platform doesn't ship `migrate down` because every shipped
migration is non-destructive (`ADD COLUMN`, `CREATE TABLE`, etc.) and
the rollback path for a bad release is "redeploy the previous
container image; the schema stays."

For an emergency *data* rollback (you ran a migration and want to
revert), restore from a `relic-api backup` taken before the upgrade.

## When upgrades go sideways

| Symptom | Diagnose | Fix |
|---|---|---|
| `relic-api migrate up` fails on a specific file | Read the SQL in the named file; check if a prior partial run left state inconsistent | The migration is meant to be idempotent — file an issue with the SQL error, then manually finish the partial step in `psql` and `INSERT INTO schema_migrations` to mark applied |
| `/v1/version` returns the old `schema_version` after `migrate up` | `migrate up` failed silently in docker-compose's migrate service | Run it explicitly: `docker compose exec relic-api /bin/relic-api migrate up` |
| `relic-governance` won't start after upgrade | Schema version requirement mismatch | Run `migrate up`; if still failing, check the governance worker's logs for the specific column it expects |
| Clients (CLI, dashboard) start showing 401 after upgrade | JWT claim shape changed; tokens issued by the old version are still in clients' storage | Log out / clear localStorage; reissue API keys |
