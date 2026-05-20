# Performance

Numeric capacity targets, methodology, and the most recent measured
results. Re-run quarterly with `test/load/*.js`. History lives in
[`PERFORMANCE_HISTORY.md`](./PERFORMANCE_HISTORY.md).

## Methodology

- Stack: dedicated load-test deployment (`therelic-api-load` on Fly.io)
  + Neon load-test branch + Cloudflare R2 load-test bucket. Provisioned
  by `test/load/setup.sh`, torn down by `test/load/teardown.sh`.
- Load generator: `k6`, scripts in `test/load/*.js`. One file per
  scenario; thresholds embedded so the run fails CI loud when targets
  drift.
- Output: `K6_OUT=experimental-prometheus-rw` → Grafana Cloud (free
  tier). Replace with whatever Prometheus-compatible sink you have.

## Targets

| Scenario | Result | Threshold | Notes |
|---|---|---|---|
| Ingest, sustained 10k/s for 10 min | _TBD_ | p95<200ms, errors<1% | `ingest-sustained.js` |
| Ingest, burst 50k/s for 30 s | _TBD_ | p95<500ms, errors<5% | `ingest-bursty.js` |
| Gateway, 1k concurrent sessions | _TBD_ | p95<150ms tools/list, p95<300ms tools/call | `gateway-concurrency.js` |
| Auth login, sustained 100/s | _TBD_ | ≥90% 429s after first 5/IP | `auth-login.js` |
| Mixed (80/15/5), 2k req/s for 10 min | _TBD_ | p95<250ms, errors<1% | `mixed-realistic.js` |

## How to reproduce

```bash
# 1. Provision the load-test stack (one-time)
export FLY_ORG=therelic
export LOADTEST_DATABASE_URL=postgres://...   # Neon load-test branch
export LOADTEST_S3_BUCKET=relic-traces-load
export LOADTEST_S3_ACCESS_KEY=...
export LOADTEST_S3_SECRET_KEY=...
./test/load/setup.sh

# 2. Mint an API key (after first deploy)
flyctl ssh console --app=therelic-api-load
# inside:
#   /bin/relic-api migrate up
#   psql $DATABASE_URL -c "INSERT INTO api_keys ..."  # or hit POST /v1/orgs/.../api-keys

# 3. Run each scenario
export RELIC_API_BASE=https://therelic-api-load.fly.dev
export RELIC_API_KEY=rk_...
k6 run test/load/ingest-sustained.js
k6 run test/load/ingest-bursty.js
k6 run test/load/gateway-concurrency.js
k6 run test/load/auth-login.js
k6 run test/load/mixed-realistic.js

# 4. Tear down
./test/load/teardown.sh
```

## Known limits (Phase 1)

- Ingest throughput is bottlenecked by Postgres single-writer on
  `runs` + `agents` updates. The 10k/s target assumes Neon Pro
  (auto-scaling compute). Free-tier Neon caps around 2k/s.
- Trace blob upload is rate-limited to 16 MB / request. Larger
  traces must be chunked client-side. (Tracked: ROADMAP phase 2.)
- Read replica routing (WS-2C) is required to hit 1k concurrent
  gateway sessions without saturating the primary.
- `auth-login.js`'s rate-limit test is single-IP; an attacker
  distributed across IPs is not exercised here.

## What we deliberately don't measure (yet)

- Multi-region failover latency. We're single-region today.
- Sustained throughput across multiple tenants in a noisy-neighbor
  pattern. Phase 2.
- Cold-start latency on the API process (Fly machines warm).

## When numbers should be re-measured

- After any change to `internal/storage/postgres.go` connection-pool
  config.
- After any change to the trace upload path
  (`internal/api/traces.go`).
- After Neon pricing-tier change.
- Every quarter regardless. Append the row to
  [`PERFORMANCE_HISTORY.md`](./PERFORMANCE_HISTORY.md).
