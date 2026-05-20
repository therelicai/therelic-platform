#!/usr/bin/env bash
# Run migrations against production. Backup first.
#
# Usage:
#   ./ops/migrate.sh                 # backs up, migrates, smoke-tests
#   ./ops/migrate.sh --no-backup     # skip backup (avoid in prod!)

set -euo pipefail

APP="${FLY_APP:-therelic-api}"
SKIP_BACKUP=0

for a in "$@"; do
  case "$a" in
    --no-backup) SKIP_BACKUP=1 ;;
    *) echo "unknown flag: $a"; exit 2 ;;
  esac
done

if [[ "$SKIP_BACKUP" -eq 0 ]]; then
  STAMP="$(date +%Y%m%dT%H%M%S)"
  echo "Taking backup before migration (database only) ..."
  flyctl ssh console --app="$APP" -C "/bin/relic-api backup /tmp/pre-migrate-$STAMP.tar.gz"
  echo "Backup written: /tmp/pre-migrate-$STAMP.tar.gz on the API machine."
  echo "If migration goes sideways, restore with:"
  echo "  flyctl ssh console --app=$APP -C '/bin/relic-api restore /tmp/pre-migrate-$STAMP.tar.gz'"
fi

echo "Running migrations ..."
flyctl ssh console --app="$APP" -C "/bin/relic-api migrate up"

echo "Smoke test ..."
curl -fsS "https://${APP}.fly.dev/readyz" >/dev/null
echo "OK"
