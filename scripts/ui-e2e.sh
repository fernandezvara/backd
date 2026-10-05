#!/usr/bin/env bash
# Runs the admin UI's Playwright tests (ui/admin/e2e) against the local
# example stack, for CI and for anyone who wants the same check before
# pushing. Starts what the UI needs (MongoDB, backd with the UI built in and
# nginx; not the docs site), prints the stack's logs when a test fails and
# always stops the stack.
#
#   scripts/ui-e2e.sh            # or: make ui-e2e
#
# Uses its own compose project, so it doesn't touch a running `make example`
# stack (both publish port 8443: stop that one first). Set
# PLAYWRIGHT_CHROMIUM to use a chromium binary you already have instead of
# the one `npx playwright install` downloads.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v docker >/dev/null || { echo "needs 'docker' on your PATH" >&2; exit 1; }
command -v node >/dev/null || { echo "needs 'node' on your PATH" >&2; exit 1; }

export COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-backd-ui-e2e}
ca=docker/certs/ca.crt
origin=https://localhost:8443

cleanup() {
  trap - EXIT
  docker compose down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

scripts/local-certs.sh
docker compose build functions-build backd
docker compose up -d --no-deps mongo
mongo_ready() {
  docker compose exec -T mongo mongosh --quiet --eval "try { rs.status() } catch (e) { rs.initiate({_id: 'rs0', members: [{_id: 0, host: 'mongo:27017'}]}) } if (!db.hello().isWritablePrimary) quit(1)" >/dev/null 2>&1
}
for _ in $(seq 1 60); do
  mongo_ready && break
  sleep 2
done
mongo_ready || { echo "MongoDB didn't become ready" >&2; exit 1; }
docker compose run --rm functions-build
# backd's dev mode and worker want the executor (the example realms declare
# functions); the UI itself needs neither, so bring up only what answers.
docker compose up -d --no-deps backd executor egress nginx

ready() { curl -sf --cacert "$ca" "$origin/readyz" >/dev/null; }
for _ in $(seq 1 120); do
  ready && break
  sleep 2
done
ready || {
  echo "the stack didn't become ready" >&2
  docker compose logs --tail 80 >&2
  exit 1
}

cd ui/admin
(cd ../../clients/js && npm ci --no-audit --no-fund)
npm ci --no-audit --no-fund
[ -n "${PLAYWRIGHT_CHROMIUM:-}" ] || npx playwright install --with-deps chromium
export NODE_EXTRA_CA_CERTS=$PWD/../../$ca E2E_URL=$origin
npx playwright test || {
  docker compose logs --tail 100 backd >&2
  exit 1
}
