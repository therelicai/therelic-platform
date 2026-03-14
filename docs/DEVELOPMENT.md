# The Relic Platform — Local Development

This guide covers running the control plane API, governance worker, and web app locally.

---

## Prerequisites

- **Go 1.23+** — [go.dev/dl](https://go.dev/dl/)
- **Docker** — For Postgres and MinIO (S3-compatible storage)
- **Node.js 20+** — For the React app ([nodejs.org](https://nodejs.org/))

Verify:

```bash
go version    # go1.23 or higher
docker --version
node --version  # v20 or higher
```

---

## 1. Start Local Services (Postgres + MinIO)

From the `therelic-platform` directory:

```bash
docker-compose up -d
```

This starts:

- **Postgres** on `localhost:54322` (user: `postgres`, password: `postgres`, db: `therelic`)
- **MinIO** on `localhost:9000` (API) and `localhost:9001` (console)
  - Access MinIO console: http://localhost:9001
  - Credentials: `minioadmin` / `minioadmin`

Create the MinIO bucket:

```bash
# Using mc (MinIO Client) or via the MinIO console at localhost:9001
# Or let the API create it on first use if your code supports it
```

If the API expects a pre-created bucket, create `traces` in the MinIO console.

---

## 2. Run Migrations

Apply database migrations:

```bash
# From therelic-platform root
for f in migrations/*.sql; do
  psql "postgres://postgres:postgres@localhost:54322/therelic?sslmode=disable" -f "$f"
done
```

Or with a single command:

```bash
psql "postgres://postgres:postgres@localhost:54322/therelic?sslmode=disable" \
  -f migrations/001_orgs_users_apikeys.sql \
  -f migrations/002_runs.sql \
  -f migrations/003_agents_baselines.sql \
  -f migrations/004_proposals.sql \
  -f migrations/005_trust_network.sql \
  -f migrations/006_auth_sync_audit.sql \
  -f migrations/007_rls_policies.sql
```

---

## 3. Start the API

```bash
cd therelic-platform

export DATABASE_URL="postgres://postgres:postgres@localhost:54322/therelic?sslmode=disable"
export S3_ENDPOINT="http://localhost:9000"
export S3_BUCKET="traces"
export S3_ACCESS_KEY="minioadmin"
export S3_SECRET_KEY="minioadmin"
export S3_REGION="us-east-1"
export SUPABASE_JWT_SECRET="super-secret-jwt-token-for-dev"
export PORT="8080"

go run ./cmd/relic-api
```

API runs at http://localhost:8080. Health check: http://localhost:8080/health

---

## 4. Start the Governance Worker

In a separate terminal:

```bash
cd therelic-platform

export DATABASE_URL="postgres://postgres:postgres@localhost:54322/therelic?sslmode=disable"
export S3_ENDPOINT="http://localhost:9000"
export S3_BUCKET="traces"
export S3_ACCESS_KEY="minioadmin"
export S3_SECRET_KEY="minioadmin"
export S3_REGION="us-east-1"
export ANTHROPIC_API_KEY="sk-ant-..."   # Optional for denial detection; required for intent classification

go run ./cmd/relic-governance
```

---

## 5. Start the React App

In another terminal:

```bash
cd therelic-app

# Create .env.local with:
# VITE_API_URL=http://localhost:8080
# VITE_SUPABASE_URL=https://your-project.supabase.co
# VITE_SUPABASE_ANON_KEY=your-anon-key

npm install
npm run dev
```

App runs at http://localhost:5173 (or 5174 if 5173 is in use).

---

## 6. Environment Variables for Local Dev

### API & Governance Worker

| Variable | Local Value |
|----------|-------------|
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:54322/therelic?sslmode=disable` |
| `S3_ENDPOINT` | `http://localhost:9000` |
| `S3_BUCKET` | `traces` |
| `S3_ACCESS_KEY` | `minioadmin` |
| `S3_SECRET_KEY` | `minioadmin` |
| `S3_REGION` | `us-east-1` |
| `SUPABASE_JWT_SECRET` | `super-secret-jwt-token-for-dev` |
| `PORT` | `8080` |
| `ANTHROPIC_API_KEY` | (optional) For governance intent classification |

### React App (`.env.local`)

| Variable | Local Value |
|----------|-------------|
| `VITE_API_URL` | `http://localhost:8080` |
| `VITE_SUPABASE_URL` | Your Supabase project URL |
| `VITE_SUPABASE_ANON_KEY` | Your Supabase anon key |

---

## 7. Quick Start Script

Create a `scripts/dev.sh` or use:

```bash
# Terminal 1: Infrastructure
docker-compose up -d

# Terminal 2: API
cd therelic-platform && source .env.local 2>/dev/null || true
export DATABASE_URL="${DATABASE_URL:-postgres://postgres:postgres@localhost:54322/therelic?sslmode=disable}"
export S3_ENDPOINT="${S3_ENDPOINT:-http://localhost:9000}"
export S3_BUCKET="${S3_BUCKET:-traces}"
export S3_ACCESS_KEY="${S3_ACCESS_KEY:-minioadmin}"
export S3_SECRET_KEY="${S3_SECRET_KEY:-minioadmin}"
export S3_REGION="${S3_REGION:-us-east-1}"
export SUPABASE_JWT_SECRET="${SUPABASE_JWT_SECRET:-super-secret-jwt-token-for-dev}"
go run ./cmd/relic-api

# Terminal 3: Governance (optional)
# Same env as API + ANTHROPIC_API_KEY
go run ./cmd/relic-governance

# Terminal 4: App
cd therelic-app && npm run dev
```

---

## 8. Troubleshooting

- **Port 54322 in use**: Change the Postgres port in `docker-compose.yml` and update `DATABASE_URL`
- **MinIO bucket missing**: Create `traces` in the MinIO console at http://localhost:9001
- **CORS errors**: The API allows `http://localhost:5173` and `http://localhost:5174` by default
- **Auth failures**: Ensure `SUPABASE_JWT_SECRET` matches your Supabase project's JWT secret when using Supabase Auth
