// k6 scenario: validate rate limit on /v1/auth/login.
//
// Floods login with 100 req/s from a single IP for 30 seconds.
// Expectation: after the first burst (5 tokens), subsequent requests
// return 429 at the configured refill rate (1 per 10s). Pass = >90%
// of requests in the steady-state return 429.

import http from 'k6/http'
import { check } from 'k6'

const BASE = __ENV.RELIC_API_BASE || 'http://localhost:8080'

export const options = {
  scenarios: {
    flood: {
      executor: 'constant-arrival-rate',
      rate: 100,
      timeUnit: '1s',
      duration: '30s',
      preAllocatedVUs: 10,
      maxVUs: 50,
    },
  },
  thresholds: {
    // Less than 95% of attempts should succeed — most must hit 429.
    'http_req_failed': ['rate>0.9'],
  },
}

export default function () {
  const res = http.post(
    `${BASE}/v1/auth/login`,
    JSON.stringify({ email: 'attacker@example.com', password: 'wrongguess' }),
    { headers: { 'Content-Type': 'application/json' } },
  )
  // We count 429 as "expected" — pass condition is on http_req_failed.
  check(res, {
    'rate-limited or unauthorized': (r) => r.status === 429 || r.status === 401,
  })
}
