# Migrations

The Relic Platform ships three migration folders. The runner in
`docker-compose.yml`'s `migrate` service applies them conditionally so
the same codebase deploys cleanly to vanilla Postgres, Supabase, Neon,
RDS, or any other Postgres.

| Folder | When applied | Contents |
|---|---|---|
| `migrations/` | Always | Core schema. Pure vanilla Postgres. No Supabase extensions, no `auth.users` references. |
| `migrations.supabase/` | When `RELIC_AUTH_MODE=supabase` | Triggers and helpers that depend on Supabase's `auth.users` table. |
| `migrations.rls/` | When `RELIC_RLS_ENABLED=true` | Row-level security policies. Defense-in-depth; the API filters by `org_id` at the application layer regardless. |

## Tracking

Every applied migration is recorded in `schema_migrations`. Core files
are tracked by bare filename (`006_audit_invitations.sql`). Optional
folder migrations are tracked with a folder prefix
(`supabase/001_supabase_user_sync.sql`, `rls/001_rls_policies.sql`) so
they never collide.

## Adding a migration

- Core schema change → new file in `migrations/` with the next sequence
  number. **Must be pure vanilla Postgres.** If you need an extension,
  document it and gate it.
- Supabase-specific feature → new file in `migrations.supabase/`.
- New RLS policy → new file in `migrations.rls/`. Use `DROP POLICY IF
  EXISTS … CREATE POLICY …` so re-runs are safe.

Every migration must be **idempotent**: re-running on an already-applied
database must be a no-op. Use `IF NOT EXISTS` for tables / indexes,
`CREATE OR REPLACE FUNCTION`, and the `DROP … IF EXISTS` pattern for
triggers and policies.

## Reserved namespaces

Customer-added columns must go under a `custom_` prefix. Relic will
never use that prefix. See [therelic-app/docs/CUSTOMIZATION.md](../../therelic-app/docs/CUSTOMIZATION.md)
(coming in WS-8).

## History

Migrations `001`-`005`, `008`, `010`-`012` are pure vanilla and have
always been portable. Migration `006` was split: vanilla audit /
invitations schema stays in `migrations/006_audit_invitations.sql`;
the Supabase user-sync trigger moved to
`migrations.supabase/001_supabase_user_sync.sql`. Migrations `007` and
`009` moved to `migrations.rls/001_rls_policies.sql` and
`migrations.rls/002_rls_completeness.sql` with idempotent rewrites.
Empty slots in the core sequence are intentional; renumbering would
break `schema_migrations` on existing installs.
