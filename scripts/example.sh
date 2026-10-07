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

# The realms with files (shelf, adminui) keep their storage keys as secrets: set them to the
# local MinIO's dev-only user, so uploads, previews and downloads work in the example apps.
# It needs an administrator: shelf's operator is created here (backd bootstrap, as the
# tutorial does) and adminui's signs up (its role is seeded in realm.yaml), both with the
# development password. An administrator that already has another password is left alone.
dev_password='dev-p4ssw0rd!'
api() { curl -s --cacert "$ca" -H 'Content-Type: application/json' "$@"; }
token_of() { sed -n 's/.*"token":"\([^"]*\)".*/\1/p'; }
set_storage_keys() { # realm email [password]
  local realm=$1 email=$2 password=${3:-$dev_password} token
  token=$(api -X POST "$origin/v1/$realm/_auth/login" -d "{\"email\":\"$email\",\"password\":\"$password\"}" | token_of)
  if [ -z "$token" ]; then
    echo "  (the $realm realm's storage keys were not set: this script signs in as $email with the development password, or with EXAMPLE_${realm^^}_PASSWORD if you set it; or set them by hand, see docs: Files → Connecting storage)"
    return 0
  fi
  for pair in STORAGE_ACCESS_KEY=backd-dev "STORAGE_SECRET_KEY=$dev_password"; do
    api -X PUT "$origin/v1/$realm/_admin/secrets/${pair%%=*}" -H "Authorization: Bearer $token" -d "{\"value\":\"${pair#*=}\"}" >/dev/null
  done
  echo "  Storage keys set for the $realm realm (signed in as $email)."
}
printf '%s\n' "$dev_password" | docker compose exec -T backd /backd bootstrap --realm shelf --email operator@shelf.example >/dev/null 2>&1 || true
api -X POST "$origin/v1/adminui/_auth/signup" -d "{\"email\":\"admin@adminui.example\",\"password\":\"$dev_password\"}" >/dev/null || true
set_storage_keys shelf operator@shelf.example "${EXAMPLE_SHELF_PASSWORD:-$dev_password}"
set_storage_keys adminui admin@adminui.example "${EXAMPLE_ADMINUI_PASSWORD:-$dev_password}"

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
  $origin/example/    the example apps (clients/js/examples): blog, expenses, shelf
                      (operator@shelf.example and admin@adminui.example, password dev-p4ssw0rd!, have the storage keys set)
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
