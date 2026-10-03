#!/usr/bin/env bash
# Runs the local stack (docker-compose.yml) on one origin: the API, the docs
# site with live reload and the JavaScript client's example app, all behind
# nginx on https://localhost:8443, with a certificate from a local CA
# (scripts/local-certs.sh creates it, and renews it when needed, on every
# run). Ctrl-C stops everything; data stays in the compose volumes
# (`docker compose down -v` resets it).
set -euo pipefail
cd "$(dirname "$0")/.."

for cmd in docker curl; do
  command -v "$cmd" >/dev/null || { echo "make example needs '$cmd' on your PATH" >&2; exit 1; }
done

follower=""
cleanup() {
  trap - EXIT INT TERM
  echo
  echo "Stopping…"
  if [ -n "$follower" ]; then
    pkill -P "$follower" 2>/dev/null || true   # the running `docker compose logs`
    kill "$follower" 2>/dev/null || true
  fi
  docker compose down >/dev/null 2>&1 || true
  echo "Stopped."
}
trap cleanup EXIT
trap 'cleanup; exit 0' INT TERM

scripts/local-certs.sh
ca=docker/certs/ca.crt
origin=https://localhost:8443

echo "Starting MongoDB, backd, the docs and nginx (the first run builds images)…"
docker compose up -d --build

# The docs take longest: Hugo downloads the theme on the first run.
ready() { curl -sf --cacert "$ca" "$origin/readyz" >/dev/null && curl -sf --cacert "$ca" "$origin/docs/" >/dev/null; }
for _ in $(seq 1 300); do
  ready && break
  sleep 1
done
ready || {
  docker compose logs --tail 50 >&2
  exit 1
}

# Tell how to trust the local CA, unless the system already does.
if curl -sf "$origin/readyz" >/dev/null 2>&1; then
  echo
  echo "  The local CA ($ca) is trusted by this system."
else
  cat <<MSG

  HTTPS uses a certificate from a local CA ($ca). Browsers warn
  about it until you trust it. Either accept the warning once for
  https://localhost:8443, or add the CA to your trust store:

    Debian/Ubuntu  sudo cp $ca /usr/local/share/ca-certificates/backd-local-ca.crt
                   sudo update-ca-certificates
    Fedora/RHEL    sudo cp $ca /etc/pki/ca-trust/source/anchors/backd-local-ca.crt
                   sudo update-ca-trust
    macOS          sudo security add-trusted-cert -d -r trustRoot \\
                     -k /Library/Keychains/System.keychain $ca
    Windows        certutil -addstore -f ROOT $ca   (as administrator)
    Firefox        Settings → Privacy & Security → Certificates → View
                   Certificates → Authorities → Import $ca
    Chrome/Chromium on Linux (its own store):
                   certutil -d sql:\$HOME/.pki/nssdb -A -t C,, -n "backd local CA" -i $ca
    curl           curl --cacert $ca …

  The CA's key is in docker/certs/: trust it only on this machine.
MSG
fi

cat <<MSG

  $origin/            the docs site (edits in docs/ reload the page)
  $origin/example/    the example apps (clients/js/examples): blog, expenses
  $origin/v1/…        backd's API (realms: blog, shop)
  http://localhost:9090/ Prometheus, scraping backd, the executor and egress
  http://localhost:3000/ Grafana, with backd's dashboards
  http://localhost:8080/ redirects to $origin/

  After changing examples/config: docker compose restart backd
  Press Ctrl-C to stop. Logs follow.

MSG
# Follow the logs; the follower ends when a service restarts, so start it
# again. Only Ctrl-C stops the stack.
( while :; do docker compose logs -f --since 1s 2>/dev/null || true; sleep 1; done ) &
follower=$!
while :; do sleep 3600 & wait $! || true; done
