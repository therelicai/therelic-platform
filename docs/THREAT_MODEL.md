# Threat model

This is the platform's security posture: assets, trust boundaries,
adversaries, and what each Relic component is and isn't trying to
defend against. Updated when the model changes, not on a calendar.

This is a **v1 threat model** for a self-hosted deployment with
local-auth mode. The Phase 1 OIDC / SAML / SCIM work in
[therelic-app/ROADMAP.md](../../therelic-app/ROADMAP.md) replaces
several of the v1 controls; revisit this doc when that lands.

---

## 1. What we're protecting

| Asset | Sensitivity | Where it lives |
|---|---|---|
| **Agent traces** (NDJSON + HMAC chain) | High — contains intent, parameters, redacted payloads | Postgres `runs` + S3 `s3://*/runs/*.trtrace` |
| **API keys** | High — grant trace-upload + policy-pull rights | Postgres `api_keys.key_hash` (HMAC-SHA256 hashed with `RELIC_API_KEY_PEPPER`) |
| **User passwords** (local-auth only) | High | Postgres `users.password_hash` (bcrypt, default cost) |
| **Session JWTs** (local-auth + supabase) | High — bearer auth | Client-side `localStorage`, lifetime 24h (local), Supabase-managed (supabase) |
| **Trace HMAC key** | High — anchors trace-chain integrity | `RELIC_TRACE_KEY` env, never written |
| **Policy YAML** | Medium — governance source of truth | Postgres `agents.policy_yaml`, optionally ed25519-signed |
| **Audit log** | Medium — compliance evidence | Postgres `audit_events` |
| **Telemetry pings** | None — bucketed counts only, no PII | Network egress when `RELIC_TELEMETRY=true` |

---

## 2. Trust boundaries

```
┌──────────────────────────────────────────────────────────────────┐
│                          External Internet                       │
│                                                                  │
│   ┌──────────────────┐    ┌──────────────────────────────────┐   │
│   │  Agent + relic   │    │  Operator browser                │   │
│   │  CLI (customer)  │    │  (logs into therelic-app)        │   │
│   └────────┬─────────┘    └────────────┬─────────────────────┘   │
│            │                           │                         │
│  HTTPS, Bearer rk_*       HTTPS, Bearer JWT (HS256)              │
└────────────┼───────────────────────────┼─────────────────────────┘
             ▼                           ▼
   ╔═════════════════════════════════════════════════════════════╗
   ║   TRUST BOUNDARY                                            ║
   ║                                                             ║
   ║   relic-api (Go HTTP server)                                ║
   ║   ───────────────────────────                               ║
   ║                                                             ║
   ║   - JWT verification (HS256 with RELIC_JWT_SECRET           ║
   ║     or SUPABASE_JWT_SECRET)                                 ║
   ║   - API-key validation (HMAC-SHA256 lookup against          ║
   ║     api_keys.key_hash)                                      ║
   ║   - Tenant filtering (every query takes org_id from         ║
   ║     the verified JWT/key)                                   ║
   ║   - Rate limiting (global 10/s burst 20, login 0.1/s        ║
   ║     burst 5)                                                ║
   ║   - HMAC-chain verification on every uploaded trace         ║
   ║                                                             ║
   ║   Postgres (internal network only, no public ingress)       ║
   ║                                                             ║
   ║   S3 / MinIO (internal network only, signed-URL access      ║
   ║                only via relic-api)                          ║
   ╚═════════════════════════════════════════════════════════════╝
```

**The boundary:** the operator is responsible for everything inside
the dotted box. Relic does not protect against an attacker who has
already breached the host running relic-api (root on the box,
compromised Postgres credentials, MITM on the internal network,
etc.). It also assumes the operator has not exposed Postgres or
S3/MinIO publicly.

---

## 3. Adversaries

| Adversary | Capability | What v1 defends |
|---|---|---|
| **Anonymous internet attacker** | Can hit any HTTPS endpoint on relic-api | Login brute force, trace-upload spam, JWT/key probing |
| **Compromised agent / API key holder** | Has a valid `rk_*` key for one org | Cross-tenant access (org_id filter), policy bypass (server-side chain verification) |
| **Compromised user account** | Has a valid JWT for one user | Same as above; plus action attribution via audit log |
| **Malicious insider with DB read** | Postgres credentials, can read tables | Password hashes (bcrypt), API key plaintext (not stored, only HMAC), trace integrity (chain re-verifiable from raw events independent of DB) |
| **Curious LB / network observer** | Sees TLS traffic shape | Nothing — operators terminate TLS at LB and trust their network |

**Out of scope for v1:**
- Compromised infrastructure (Postgres-root attacker, S3-bucket
  takeover, hypervisor escape). These break every assumption.
- Compromised maintainer's source tree. Supply-chain attestation is
  on the Phase 4 roadmap (Sigstore / in-toto).
- Side channels (timing, cache). Generic Go primitives.
- Denial-of-service beyond simple rate limiting.

---

## 4. Specific controls

### 4.1 Authentication

**Local mode** (`RELIC_AUTH_MODE=local`)
- Passwords hashed with `bcrypt` (Go `golang.org/x/crypto/bcrypt`),
  default cost (10 rounds). Constant-time comparison via
  `bcrypt.CompareHashAndPassword`.
- Login endpoint rate-limited at 5 attempts burst then refills 1
  token every 10 seconds, per client IP. After 5 wrong tries an
  attacker waits 10s per attempt — effectively unbounded for
  brute-force purposes.
- Uniform error response on every login failure (same 401 + same
  message for `email-not-found` and `password-mismatch`). Prevents
  account-enumeration via response timing or body.
- JWTs signed HS256 with `RELIC_JWT_SECRET` (≥32 random bytes).
  Lifetime 24h, no refresh token (operator hands out fresh
  passwords when needed).
- Bootstrap admin created from `RELIC_ADMIN_EMAIL` +
  `RELIC_ADMIN_PASSWORD` env vars only when the `users` table is
  empty. Vars can be removed after first boot.

**Supabase mode** (`RELIC_AUTH_MODE=supabase`)
- JWTs verified against `SUPABASE_JWT_SECRET`. All auth lifecycle
  (rotation, MFA, social) is Supabase's responsibility.

**API keys**
- Format: `rk_<32-byte-hex>`. 256 bits of entropy.
- Stored as `HMAC-SHA256(RELIC_API_KEY_PEPPER, plaintext)`. Pepper
  is per-deployment; rotation requires reissuing every key.
- Legacy keys (no pepper) supported on a fallback path so
  pre-pepper deployments keep working during rollout.
- Revocation: `api_keys.revoked_at` column; lookups skip revoked.

### 4.2 Authorization

- Every authenticated route extracts `org_id` from the verified
  token's `app_metadata.org_id` claim. The HTTP handler is
  responsible for passing that into every query.
- Cross-tenant access requires a second `org_id` parameter to
  match the caller's. Audited via `internal/api/audit.go`'s
  `requireOrg` helper.
- RLS (`migrations.rls/`) is available as defense-in-depth when
  `RELIC_RLS_ENABLED=true`. The Go API connects with a privileged
  role and sets `SET LOCAL request.jwt.claims = …` per request so
  policies match the caller's org_id.

### 4.3 Trace integrity

- Every action event carries `hmac = HMAC-SHA256(RELIC_TRACE_KEY,
  prev_hmac || event_canonical_form)`.
- Server recomputes the chain on every upload via
  `internal/trace.VerifyChain`. Mismatch → `chain_verified: false`
  flagged on the run.
- `RELIC_REQUIRE_SEALED_TRACES=true` rejects uploads whose chain
  doesn't verify. Default off so legacy clients during rollout
  still work.

### 4.4 Transport

- TLS expected to terminate at a load balancer / reverse proxy in
  front of relic-api. The relic-api process speaks plain HTTP.
- CORS allowlist via `ALLOWED_ORIGINS` (comma-separated). No
  wildcard support; explicit origins only.
- CSP on `therelic-app` restricts `connect-src` to the configured
  API URL + Supabase.

### 4.5 Rate limits

- Global per-IP: 10 requests/sec, burst 20. Applied to every
  route.
- Login per-IP: 0.1 requests/sec, burst 5. Stacks on top of global.
- Source IP derived from `X-Forwarded-For` (if set) or the
  TCP-level remote address (port stripped). **Operators behind a
  proxy must terminate `X-Forwarded-For` at the proxy** —
  otherwise an attacker can spoof it.

### 4.6 Audit + observability

- Every privileged action (create org, create API key, approve
  proposal, etc.) writes to `audit_events`. Read-only via
  `GET /v1/audit-events`.
- Structured JSON logs on stdout. Includes request_id (correlation),
  not user PII.

---

## 5. Known gaps blocking enterprise production

These are the controls Phase 1 / Phase 2 of ROADMAP.md adds. Until
they land, the platform is appropriate for self-host adopters but
not for SOC 2 / ISO 27001-gated customers.

- **No SAML / SCIM / OIDC.** Phase 1.
- **No MFA on login.** Phase 1 (via OIDC).
- **No password complexity / lockout policy.** v1 enforces only
  8-character minimum.
- **No session revocation.** A leaked JWT is valid until expiry
  (24h in local mode).
- **No CSRF protection.** Cookies aren't used; bearer-only auth.
  CSRF risk is low but a hosted free tier would want SameSite
  + double-submit anyway.
- **No HTTP security header middleware** (`X-Frame-Options`,
  `Strict-Transport-Security`, `X-Content-Type-Options`). Expected
  to be added at the reverse proxy in v1; native middleware on
  the roadmap.
- **No signed S3 URLs for trace download.** Operator-only via
  Postgres lookup; sufficient for self-host, not for multi-tenant
  hosted.
- **No secrets-scanning on inbound traces.** The redactor masks
  declared keys + headers, but operators must define them
  correctly in their policy.
- **No supply-chain attestation.** Phase 4 (Sigstore + in-toto).

---

## 6. Reporting a vulnerability

`SECURITY.md` in [therelic](https://github.com/therelicai/therelic)
is the canonical disclosure channel. Mirror policies apply to this
repo. Coordinate disclosure with `security@therelic.dev`.
