// k6 scenario: sustained trace ingest.
//
// Goal: 10k events/sec sustained for 10 minutes. Pass = <1% error
// rate, p95 < 200ms. Run against the dedicated load-test stack (see
// test/load/setup.sh); never against prod.
//
// Usage:
//   K6_OUT=experimental-prometheus-rw \
//   RELIC_API_BASE=https://api.staging.therelic.dev \
//   RELIC_API_KEY=rk_... \
//   k6 run test/load/ingest-sustained.js

import http from 'k6/http'
import { check } from 'k6'

const BASE = __ENV.RELIC_API_BASE || 'http://localhost:8080'
const KEY = __ENV.RELIC_API_KEY || ''

export const options = {
  scenarios: {
    ingest: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 10000),
      timeUnit: '1s',
      duration: __ENV.DURATION || '10m',
      preAllocatedVUs: 100,
      maxVUs: 500,
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<200'],
    http_req_failed: ['rate<0.01'],
  },
}

function buildTrace() {
  // One synthetic trace per request. Real traces are gzip+NDJSON; for
  // load we just send a small JSON shape the trace upload endpoint
  // accepts. Skip HMAC chain — the platform records `has_chain=false`
  // and the loadtest only exercises the write path.
  return JSON.stringify({
    run_id: `loadtest-${__VU}-${__ITER}-${Date.now()}`,
    agent_name: 'loadtest-agent',
    started_at: new Date().toISOString(),
    events: [
      { kind: 'tool_call', tool: 'noop', ts: new Date().toISOString() },
    ],
  })
}

export default function () {
  const res = http.post(`${BASE}/v1/traces`, buildTrace(), {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${KEY}`,
    },
  })
  check(res, { 'status is 2xx': (r) => r.status >= 200 && r.status < 300 })
}
