# OpenTelemetry export

Relic ships an OTLP/HTTP exporter that sends spans + metrics to any
OTEL-compatible backend (Splunk, Datadog, Elastic, Honeycomb, New
Relic, Grafana Cloud, self-hosted Tempo/Loki/Mimir).

## Configuration

Set these env vars on the API process:

```
RELIC_OTEL_ENABLED=true                         # explicit opt-in; default false
RELIC_OTEL_ENDPOINT=https://api.honeycomb.io
RELIC_OTEL_AUTH_HEADER="x-honeycomb-team:..."   # one header pair
RELIC_OTEL_SERVICE_NAME=relic-api               # default
RELIC_OTEL_RESOURCE_ATTRIBUTES="env=prod,region=iad"
```

When `RELIC_OTEL_ENABLED` is unset, the exporter does nothing — no
network calls, no allocations, no log lines. Safe default for
self-hosters.

## What gets emitted

### Spans

| Name | Attributes |
|---|---|
| `auth.login` | `org_id`, `user_id`, `provider`, `result` (success/failure) |
| `trace.ingest` | `org_id`, `agent_name`, `event_count`, `duration_ms` |
| `policy.evaluate` | `org_id`, `agent_name`, `tool`, `decision` |

### Metrics

| Name | Type | Labels |
|---|---|---|
| `relic_policy_decisions_total` | counter | `decision`, `org` |
| `relic_trace_events_ingested_total` | counter | `org` |
| `relic_auth_login_total` | counter | `provider`, `result` |

These complement the existing Prometheus surface at `/metrics` —
operators picking Prometheus over OTLP don't need this exporter at
all.

## Per-backend configuration snippets

### Splunk Observability Cloud

```
RELIC_OTEL_ENABLED=true
RELIC_OTEL_ENDPOINT=https://ingest.<realm>.signalfx.com
RELIC_OTEL_AUTH_HEADER="X-SF-Token:<your-token>"
```

### Datadog

```
RELIC_OTEL_ENABLED=true
RELIC_OTEL_ENDPOINT=https://trace.agent.datadoghq.com
RELIC_OTEL_AUTH_HEADER="DD-API-KEY:<your-key>"
```

### Honeycomb

```
RELIC_OTEL_ENABLED=true
RELIC_OTEL_ENDPOINT=https://api.honeycomb.io
RELIC_OTEL_AUTH_HEADER="x-honeycomb-team:<your-team>"
```

### Elastic Cloud

```
RELIC_OTEL_ENABLED=true
RELIC_OTEL_ENDPOINT=<elastic-apm-server-url>
RELIC_OTEL_AUTH_HEADER="Authorization:Bearer <token>"
```

### New Relic

```
RELIC_OTEL_ENABLED=true
RELIC_OTEL_ENDPOINT=https://otlp.nr-data.net
RELIC_OTEL_AUTH_HEADER="api-key:<your-license-key>"
```

### Grafana Cloud

```
RELIC_OTEL_ENABLED=true
RELIC_OTEL_ENDPOINT=https://otlp-gateway-<region>.grafana.net
RELIC_OTEL_AUTH_HEADER="Authorization:Basic <base64(instance:token)>"
```

## Verifying the export

After enabling, hit a few endpoints and check the backend:

```bash
curl -fsS https://api.therelic.dev/v1/version    # generates an HTTP span
# Then in your backend UI, search for service:relic-api in the last 5 min.
```

Spans are batched every 2s. Metrics flush every 30s.

## Privacy

The exporter sends:
- Resource attributes you configured.
- Span names + the attributes listed above.
- Counter values + labels.

It does NOT send:
- Trace bodies.
- User passwords, API keys, or session tokens.
- Audit-log metadata bodies.
- Any data from the runs.storage_key column (blob locations).

If you're running in a regulated environment and need cell-level
auditing of what egresses, point the exporter at your own collector
(e.g. self-hosted OpenTelemetry Collector) and inspect the wire
traffic with `tcpdump`.
