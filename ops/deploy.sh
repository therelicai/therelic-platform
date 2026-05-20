#!/usr/bin/env bash
# Deploy the API to production.
#
# CI runs this automatically on push to main (.github/workflows/deploy-api.yml).
# Operators only invoke this manually for hotfixes or when CI is wedged.

set -euo pipefail

APP="${FLY_APP:-therelic-api}"
CONFIG="fly.production.toml"

if ! command -v flyctl >/dev/null 2>&1; then
  echo "flyctl not found — install from https://fly.io/docs/flyctl/install/"
  exit 1
fi

echo "Deploying $APP using $CONFIG ..."
flyctl deploy --config "$CONFIG" --app="$APP" --remote-only

echo "Running migrations ..."
flyctl ssh console --app="$APP" -C "/bin/relic-api migrate up"

echo "Smoke test ..."
HTTP_STATUS="$(curl -s -o /dev/null -w '%{http_code}' "https://${APP}.fly.dev/readyz")"
if [[ "$HTTP_STATUS" != "200" ]]; then
  echo "WARN: /readyz returned $HTTP_STATUS"
  exit 1
fi
echo "OK — /readyz returns 200"
