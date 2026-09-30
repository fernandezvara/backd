#!/bin/sh
# Proves F4's egress proxy and network placement: a function can't reach
# an undeclared host, a declared cloud-metadata address, MongoDB (by fetch
# or by raw TCP, even through a declared name that resolves to it), or
# anything through an allowlisted name that resolves to a loopback address
# — except that raw TCP inevitably reaches the executor's own loopback,
# where its /invoke still refuses an unauthenticated request.
# Needs docker (or podman) compose, curl and openssl.
#
#   docker/egress-test/test.sh
set -eu
cd "$(dirname "$0")"

project=backd-egress-test
export EXECUTOR_TOKEN=$(openssl rand -hex 32)
export CALLBACK_KEY=$(openssl rand -hex 32)
export EGRESS_KEY=$(openssl rand -hex 32)
export PORT=${PORT:-18092}

compose() { docker compose -p "$project" -f docker-compose.yml "$@"; }
cleanup() {
  status=$?
  if [ "${KEEP:-}" = 1 ]; then
    echo "stack kept: docker compose -p $project -f docker-compose.yml ..."
  else
    compose down -v >/dev/null 2>&1 || true
    rm -rf config/probe/app/_functions/.build config/probe/app/_functions/probe/deno.lock
  fi
  exit $status
}
trap cleanup EXIT INT TERM

echo "building images and the probe function's bundle..."
compose build >/dev/null
compose run --rm functions-build

compose up -d mongo backd executor egress

base=http://localhost:$PORT
fails=0
pass() { echo "ok   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }

echo "waiting for the stack..."
resp=""
for i in $(seq 1 60); do
  resp=$(curl -s -m 3 -X POST "$base/v1/probe/app/_func/probe" -H 'Content-Type: application/json' -d '{}' 2>/dev/null || true)
  case "$resp" in
  *'"results"'*) break ;;
  esac
  sleep 1
done
echo "$resp"
case "$resp" in
*'"results"'*) ;;
*)
  echo "FAIL the stack never became ready"
  compose logs
  exit 1
  ;;
esac

check() { # check <description> <substring the response must contain>
  case "$resp" in
  *"$2"*) pass "$1" ;;
  *) fail "$1" ;;
  esac
}

# A fetch egress blocks completes as a normal HTTP round trip (egress IS
# the origin server, from fetch()'s point of view, for a plain http://
# request): "reached": true with a 403 status, not a thrown exception.
check "layer 1: an undeclared host is refused" '"test":"unlisted-host","reached":false'
check "layer 2: a declared metadata address is refused (fetch, 403 from egress)" '"test":"metadata","reached":true,"status":403'
check "layer 2: a declared name resolving to MongoDB is refused (fetch, 403 from egress)" '"test":"mongo-clone-fetch","reached":true,"status":403'
check "layer 3: the same address has no route at all (raw TCP, bypasses egress)" '"test":"mongo-clone-rawtcp","reached":false'
check "layer 2: a declared name resolving to loopback is refused (fetch, 403 from egress)" '"test":"sneaky-loopback-fetch","reached":true,"status":403'
check "the executor's own /invoke refuses an unauthenticated request (raw TCP reaches it; auth refuses it)" \
  '"test":"sneaky-executor-rawtcp","reached":true,"status_line":"HTTP/1.1 401'

if [ "$fails" -eq 0 ]; then
  echo "ALL PASS"
else
  echo "$fails FAILED"
  compose logs
  exit 1
fi
