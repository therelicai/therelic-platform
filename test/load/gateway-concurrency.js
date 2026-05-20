// k6 scenario: 1k concurrent gateway sessions.
//
// Holds 1000 VUs each calling tools/list + tools/call against the
// gateway endpoints. Pass = p95(tools/list) < 150ms, p95(tools/call)
// < 300ms.

import http from 'k6/http'
import { check, sleep } from 'k6'

const BASE = __ENV.RELIC_API_BASE || 'http://localhost:8080'
const KEY = __ENV.RELIC_API_KEY || ''

export const options = {
  scenarios: {
    gateway: {
      executor: 'constant-vus',
      vus: Number(__ENV.VUS || 1000),
      duration: __ENV.DURATION || '5m',
    },
  },
  thresholds: {
    'http_req_duration{name:tools_list}': ['p(95)<150'],
    'http_req_duration{name:tools_call}': ['p(95)<300'],
    http_req_failed: ['rate<0.01'],
  },
}

export default function () {
  const list = http.get(`${BASE}/v1/agents`, {
    headers: { Authorization: `Bearer ${KEY}` },
    tags: { name: 'tools_list' },
  })
  check(list, { 'list 2xx': (r) => r.status >= 200 && r.status < 300 })

  // Simulate one tool invocation per session iteration. The exact
  // endpoint may vary by gateway shape; this hits /policy_sets/resolve
  // which is the heaviest read in the policy path.
  const call = http.post(
    `${BASE}/v1/policy_sets/resolve`,
    JSON.stringify({ selector: { match: { '*': '*' } } }),
    {
      headers: {
        'Content-Type': 'application/json',
        Authorization: `Bearer ${KEY}`,
      },
      tags: { name: 'tools_call' },
    },
  )
  check(call, { 'call 2xx': (r) => r.status >= 200 && r.status < 300 })
  sleep(0.5)
}
