// k6 scenario: bursty ingest.
//
// Spikes to 50k events/sec for 30 seconds, then idles. Pass = errors
// < 5%, p95 < 500ms. Validates that the autoscaler / load balancer
// doesn't drop the burst on the floor.

import http from 'k6/http'
import { check } from 'k6'

const BASE = __ENV.RELIC_API_BASE || 'http://localhost:8080'
const KEY = __ENV.RELIC_API_KEY || ''

export const options = {
  scenarios: {
    burst: {
      executor: 'ramping-arrival-rate',
      startRate: 100,
      timeUnit: '1s',
      preAllocatedVUs: 200,
      maxVUs: 1000,
      stages: [
        { duration: '20s', target: 100 },
        { duration: '10s', target: 50000 },
        { duration: '30s', target: 50000 },
        { duration: '20s', target: 100 },
      ],
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<500'],
    http_req_failed: ['rate<0.05'],
  },
}

export default function () {
  const body = JSON.stringify({
    run_id: `burst-${__VU}-${__ITER}-${Date.now()}`,
    agent_name: 'burst-agent',
    started_at: new Date().toISOString(),
    events: [{ kind: 'tool_call', tool: 'noop', ts: new Date().toISOString() }],
  })
  const res = http.post(`${BASE}/v1/traces`, body, {
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${KEY}` },
  })
  check(res, { '2xx': (r) => r.status >= 200 && r.status < 300 })
}
