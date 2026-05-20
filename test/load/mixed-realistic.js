// k6 scenario: realistic mixed traffic.
//
// 80% trace ingest, 15% reads (list traces / agents / policies),
// 5% policy writes. Mirrors the steady-state traffic of a real
// tenant. Pass = error rate < 1%, p95 < 250ms.

import http from 'k6/http'
import { check } from 'k6'

const BASE = __ENV.RELIC_API_BASE || 'http://localhost:8080'
const KEY = __ENV.RELIC_API_KEY || ''
const HDR = { 'Content-Type': 'application/json', Authorization: `Bearer ${KEY}` }

export const options = {
  scenarios: {
    mix: {
      executor: 'constant-arrival-rate',
      rate: Number(__ENV.RATE || 2000),
      timeUnit: '1s',
      duration: __ENV.DURATION || '10m',
      preAllocatedVUs: 100,
      maxVUs: 500,
    },
  },
  thresholds: {
    http_req_duration: ['p(95)<250'],
    http_req_failed: ['rate<0.01'],
  },
}

function ingest() {
  return http.post(
    `${BASE}/v1/traces`,
    JSON.stringify({
      run_id: `mix-${__VU}-${__ITER}-${Date.now()}`,
      agent_name: 'mix-agent',
      started_at: new Date().toISOString(),
      events: [{ kind: 'tool_call', tool: 'noop', ts: new Date().toISOString() }],
    }),
    { headers: HDR },
  )
}

function listTraces() {
  return http.get(`${BASE}/v1/traces?limit=50`, { headers: HDR })
}

function listAgents() {
  return http.get(`${BASE}/v1/agents`, { headers: HDR })
}

function policyWrite() {
  return http.post(
    `${BASE}/v1/policy_sets`,
    JSON.stringify({
      name: `loadtest-set-${__VU}-${__ITER}`,
      selector: { match: { agent: '*' } },
      rules: [{ tool: 'noop', decision: 'allow' }],
    }),
    { headers: HDR },
  )
}

export default function () {
  const r = Math.random()
  let res
  if (r < 0.8) {
    res = ingest()
  } else if (r < 0.875) {
    res = listTraces()
  } else if (r < 0.95) {
    res = listAgents()
  } else {
    res = policyWrite()
  }
  check(res, { '2xx': (r) => r.status >= 200 && r.status < 300 })
}
