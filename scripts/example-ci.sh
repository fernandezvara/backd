#!/usr/bin/env bash
# Runs the example attack scripts against the local stack, for CI and for
# anyone who wants the same check before pushing. It starts what the scripts
# need (MongoDB, backd with its worker, the executor, egress and nginx; not
# the docs site), runs them, prints the stack's logs when one fails and
# always stops the stack. A script exits non-zero when an attack succeeds
# that should be stopped, or when a known hole is closed without the
# documentation (and the script's expectations) saying so.
#
#   scripts/example-ci.sh            # every script
#   make example-attacks
#
# Uses its own compose project, so it doesn't touch a running `make example`
# stack (both publish port 8443: stop that one first).
set -euo pipefail
cd "$(dirname "$0")/.."

command -v docker >/dev/null || { echo "needs 'docker' on your PATH" >&2; exit 1; }
command -v node >/dev/null || { echo "needs 'node' on your PATH" >&2; exit 1; }

export COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-backd-attacks}
ca=docker/certs/ca.crt
origin=https://localhost:8443

cleanup() {
  trap - EXIT
  docker compose down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

scripts/local-certs.sh
# Step by step and without dependencies: the docs site, which nginx would
# pull in, isn't needed (nginx resolves its upstreams per request).
docker compose build functions-build backd executor egress
docker compose up -d --no-deps mongo
# A single-node replica set: initiate it (the compose health check does the
# same) and wait for the primary.
mongo_ready() {
  docker compose exec -T mongo mongosh --quiet --eval "try { rs.status() } catch (e) { rs.initiate({_id: 'rs0', members: [{_id: 0, host: 'mongo:27017'}]}) } if (!db.hello().isWritablePrimary) quit(1)" >/dev/null 2>&1
}
for _ in $(seq 1 60); do
  mongo_ready && break
  sleep 2
done
mongo_ready || { echo "MongoDB didn't become ready" >&2; exit 1; }
docker compose run --rm functions-build
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

export NODE_EXTRA_CA_CERTS=$PWD/$ca
failed=()
for script in \
  clients/js/examples/expenses-without-functions/hack.js \
  clients/js/examples/expenses-with-functions/hack.js \
  clients/js/examples/workshop/tour.js \
  clients/js/examples/attacks/account.js; do
  [ -f "$script" ] || continue
  echo
  echo "=== $script"
  node "$script" || failed+=("$script")
done

if [ ${#failed[@]} -gt 0 ]; then
  echo >&2
  printf 'FAILED: %s\n' "${failed[@]}" >&2
  docker compose logs --tail 100 backd >&2
  exit 1
fi
echo
echo "Every attack script gave the documented result."
