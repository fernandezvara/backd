#!/usr/bin/env bash
# Starts backd + MongoDB, creates API keys, runs the client's integration
# tests against them, and tears everything down.
set -euo pipefail
cd "$(dirname "$0")/../../../.."
compose() { docker compose -f docker-compose.js.yml "$@"; }
trap 'compose down -v >/dev/null 2>&1 || true' EXIT

compose up -d --build
for _ in $(seq 1 90); do
  curl -sf http://127.0.0.1:18080/readyz >/dev/null && break
  sleep 1
done
curl -sf http://127.0.0.1:18080/readyz >/dev/null || { compose logs backd; exit 1; }

# Each realm gets a first administrator (bootstrap), who logs in with the
# CLI in the backd container and creates an admin API key for the tests.
# Key names are unique per run, in case data survives from an earlier run.
run_id=$(date +%s)
key() {
  local out err
  err=$(mktemp)
  printf 'dev-p4ssw0rd!\n' | compose exec -T backd /backd bootstrap --realm "$1" --email ops@example.com --role ops >/dev/null 2>"$err" ||
    grep -q 'already has an administrator' "$err" || { cat "$err" >&2; return 1; }
  printf 'dev-p4ssw0rd!\n' | compose exec -T backd /backd login --realm "$1" --url http://localhost:8080 --email ops@example.com >/dev/null 2>"$err" || { cat "$err" >&2; return 1; }
  out=$(compose exec -T backd /backd apikey create --realm "$1" --name "tests-$run_id" --role admin 2>"$err") || { cat "$err" >&2; return 1; }
  rm -f "$err"
  echo "$out" | grep -o 'bdk_[A-Za-z0-9_-]*' | head -1
}
export BACKD_URL=http://127.0.0.1:18080
BACKD_API_KEY=$(key itest)
BACKD_INVITE_API_KEY=$(key invite)
BACKD_MAIL_API_KEY=$(key mail)
export BACKD_API_KEY BACKD_INVITE_API_KEY BACKD_MAIL_API_KEY

cd clients/js
npm run test:integration
