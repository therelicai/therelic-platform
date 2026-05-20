#!/usr/bin/env bash
# Provision a dedicated load-test stack on Fly.io.
#
# Spins up a separate `therelic-api-load` app + Neon load-test branch
# so the load test doesn't share resources with prod. Teardown is in
# teardown.sh. The numbers we publish in docs/PERFORMANCE.md come
# from runs against this stack.
#
# Prerequisites:
#   - flyctl logged in
#   - psql installed (for Neon branch creation if you script it; the
#     simpler path is to create the branch manually in the Neon
#     console and copy DATABASE_URL into a local .env.loadtest)
#
# Usage:
#   ./test/load/setup.sh

set -euo pipefail

APP="therelic-api-load"
REGION="${FLY_REGION:-iad}"

echo "Creating Fly app: $APP in $REGION"
flyctl apps create "$APP" --org="${FLY_ORG:-therelic}" || true

if [[ -z "${LOADTEST_DATABASE_URL:-}" ]]; then
  echo "ERROR: set LOADTEST_DATABASE_URL (Neon load-test branch) before running."
  echo "       Create a branch in the Neon console and copy the connection string."
  exit 1
fi
if [[ -z "${LOADTEST_S3_BUCKET:-}" ]]; then
  echo "ERROR: set LOADTEST_S3_BUCKET (Cloudflare R2 bucket for load tests)."
  exit 1
fi

echo "Setting secrets..."
flyctl secrets set --app="$APP" \
  DATABASE_URL="$LOADTEST_DATABASE_URL" \
  RELIC_AUTH_MODE=local \
  RELIC_JWT_SECRET="$(openssl rand -hex 32)" \
  RELIC_TRACE_KEY="$(openssl rand -hex 32)" \
  RELIC_API_KEY_PEPPER="$(openssl rand -hex 32)" \
  S3_ENDPOINT="${LOADTEST_S3_ENDPOINT:-}" \
  S3_BUCKET="$LOADTEST_S3_BUCKET" \
  S3_ACCESS_KEY="${LOADTEST_S3_ACCESS_KEY:-}" \
  S3_SECRET_KEY="${LOADTEST_S3_SECRET_KEY:-}" \
  S3_REGION="${LOADTEST_S3_REGION:-auto}" \
  RELIC_ADMIN_EMAIL=load@therelic.dev \
  RELIC_ADMIN_PASSWORD="$(openssl rand -hex 16)"

echo "Deploying $APP from current directory..."
flyctl deploy --config fly.production.toml --app="$APP" --remote-only
flyctl ssh console --app="$APP" -C "/bin/relic-api migrate up"

echo "Load-test stack ready: https://${APP}.fly.dev"
echo
echo "Next: provision an API key, then export it for k6 runs:"
echo "  flyctl ssh console --app=$APP"
echo "  # inside: psql \$DATABASE_URL"
echo "  export RELIC_API_BASE=https://${APP}.fly.dev"
echo "  export RELIC_API_KEY=rk_..."
