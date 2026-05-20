#!/usr/bin/env bash
# Tear down the load-test stack provisioned by setup.sh.
# Leaves the Neon load-test branch in place (cheap to keep around).

set -euo pipefail

APP="therelic-api-load"

echo "Destroying Fly app: $APP"
flyctl apps destroy "$APP" --yes || true
echo "Done. Reminder: drop the Neon load-test branch via the Neon console if you no longer need it."
