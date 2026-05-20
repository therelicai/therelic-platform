# Service-level objectives

Target SLOs for the hosted `api.therelic.dev` deployment. Self-host
operators can adopt these as a starting point, but the numbers below
assume Neon Pro + Fly.io's iad region + Cloudflare R2.

## SLIs

| SLI | Target | Measurement window | Error budget |
|---|---|---|---|
| Availability (`/readyz` returns 200) | 99.9% | rolling 30 days | 43 min/month |
| API p95 latency, read endpoints | <200 ms | rolling 1 hour | n/a |
| Trace ingest p95 latency | <500 ms | rolling 1 hour | n/a |
| Background worker freshness (proposals, retention) | <5 min lag | rolling 30 days | n/a |
| SSE feed delivery (livefeed, policyfeed) | <2 s end-to-end | rolling 1 hour | n/a |

## How we measure

- **Availability** — UptimeRobot hits `/readyz` from 5 regions every
  5 min. A degraded response (db or s3 check fail) counts as down.
- **Latency** — `relic_api_request_duration_seconds` Prometheus
  histogram (already wired). The route label is bounded to chi's
  RoutePattern (≤ 50 unique routes), so cardinality stays sane.
- **Background freshness** — `relic_retention_last_run_at` gauge
  vs. `time.Now()`. If the gauge is older than 5 minutes, alert.
- **SSE delivery** — internal end-to-end metric: emit a synthetic
  event every minute on the live feed, measure receive time on a
  permanent test subscriber.

## Burn-rate alerting

A standard SRE setup: alert when the error budget is being consumed
at a rate that would exhaust it before the window closes.

- Fast burn: 1-hour window, threshold 14.4× normal rate → page.
- Slow burn: 6-hour window, threshold 6× normal rate → ticket.

(Exact alert wiring lives in the Grafana / Honeycomb dashboard, not
in this repo.)

## When SLOs should be reviewed

- After a major change to the trace ingest path.
- After moving the Postgres tier (e.g. Neon Launch → Pro).
- Every quarter regardless. Append SLO history to a new file when
  the targets change.

## What's deliberately not an SLO yet

- Evidence-pack generation latency (WS-3B). It's an admin-triggered
  batch operation; we measure success rate, not latency, during
  Phase 1.
- OTEL export reliability. Best-effort by design; missed spans are
  not a customer-visible failure.
- Marketplace search p95. Low traffic; SLO would over-fit noise.
