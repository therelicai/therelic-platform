# High availability

The platform's HA story in v1 is:

- **Stateless API process.** Multiple `relic-api` machines behind a
  load balancer. Lose any one machine, the next request lands
  somewhere else.
- **Postgres = Neon Pro** for prod. Branch + replica is Neon's
  responsibility; we route reads to the replica when one is
  configured.
- **Object store = Cloudflare R2**, multi-region by default.
- **Background workers** (retention, livefeed listener, policyfeed
  listener) run on every API replica. They're idempotent — running
  twice doesn't double-reap or double-broadcast.

## Read/write split

When `DATABASE_REPLICA_URL` is set, the API builds a second `pgxpool`
against the replica and routes reads through `db.Readonly()`. Writes
go to `db.Pool()` (the primary). Failover is handled by Neon — the
operator only needs to point both env vars at the right endpoint.

Handlers decide read pool per-call. Default: primary. Opt into the
replica when:

- Read-after-write isn't required for the request's semantics.
- The read is large or repetitive (audit-log listing, baseline
  computation, simulator job lookups).

Handlers that explicitly need read-after-write (post-create echo,
session validation) keep using the primary so the user never sees a
stale row they just wrote.

## Failover playbook

### Replica unavailable

Symptom: `/readyz` returns 503 with `db_replica: fail`.

1. `flyctl logs --app=therelic-api | grep replica` — confirm the
   replica pool is what's failing, not the primary.
2. Either:
   - Wait for the Neon side to recover (transient connection blip).
   - Or unset `DATABASE_REPLICA_URL` and redeploy; reads fall back
     to the primary at the cost of higher primary load.
3. After Neon recovers, re-set the secret and redeploy.

### Primary unavailable

Symptom: `/readyz` returns 503 with `db: fail` and writes are 500s.

1. Confirm the Neon primary is down via the Neon dashboard.
2. If Neon has promoted the replica automatically, update
   `DATABASE_URL` to the new primary endpoint and redeploy.
3. If Neon hasn't promoted, escalate. Manual promotion via the Neon
   console; takes ~1 minute.

### API process stuck

Symptom: requests time out on /readyz but Postgres is fine.

1. `flyctl machines list --app=therelic-api` — confirm machine count.
2. `flyctl machine restart <id>` to bounce a single machine, or
   `flyctl deploy --strategy=rolling --remote-only` to bounce all.
3. If the issue is repeatable, capture `/metrics` + `flyctl logs`
   and file a bug.

## Self-host operators

For self-hosters who don't run Neon: a single Postgres primary is fine
for the OSS install. Document a read-replica only when you grow past
one machine's worth of reads.

The simplest replica pattern is:
- Primary: write-only.
- Streaming replica via `pg_basebackup` + WAL streaming.
- App reads from the replica when `DATABASE_REPLICA_URL` is set.
- Lag monitoring via `pg_stat_replication.replay_lag`.
