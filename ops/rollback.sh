#!/usr/bin/env bash
# Roll the API back one release. Use --to v<N> to target a specific
# release.

set -euo pipefail

APP="${FLY_APP:-therelic-api}"
TARGET=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --to) TARGET="$2"; shift 2 ;;
    *) echo "unknown flag: $1"; exit 2 ;;
  esac
done

if [[ -z "$TARGET" ]]; then
  TARGET="$(flyctl releases --app="$APP" --json | jq -r '.[1].image_ref' 2>/dev/null)"
  if [[ -z "$TARGET" || "$TARGET" == "null" ]]; then
    echo "Could not determine previous release. Pass --to v<N> explicitly."
    exit 1
  fi
fi

echo "Rolling $APP back to $TARGET ..."
flyctl deploy --app="$APP" --image "$TARGET" --remote-only

echo "Smoke test ..."
curl -fsS "https://${APP}.fly.dev/readyz" >/dev/null
echo "OK"
