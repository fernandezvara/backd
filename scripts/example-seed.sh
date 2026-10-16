#!/usr/bin/env bash
# Seeds the example realms of a running stack (make example, make dev-examples): the realms with
# files (shelf, adminui) keep their storage keys as secrets, set to the local MinIO's dev-only user,
# so uploads, previews and downloads work in the example apps. It needs an administrator: shelf's
# operator is created here (backd bootstrap, as the tutorial does) and adminui's signs up (its
# role is seeded in realm.yaml), both with the development password. An administrator that
# already has another password is left alone.
#
# Environment: ORIGIN (https://localhost:8443), CA (docker/certs/ca.crt) and BACKD_EXEC, the
# command that runs the backd binary in the stack's API container, for example
#   "docker compose exec -T backd /backd"
set -euo pipefail
cd "$(dirname "$0")/.."
origin=${ORIGIN:-https://localhost:8443}
ca=${CA:-docker/certs/ca.crt}
backd_exec=${BACKD_EXEC:?set BACKD_EXEC to the command that runs backd in the API container}

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
printf '%s\n' "$dev_password" | $backd_exec bootstrap --realm shelf --email operator@shelf.example >/dev/null 2>&1 || true
api -X POST "$origin/v1/adminui/_auth/signup" -d "{\"email\":\"admin@adminui.example\",\"password\":\"$dev_password\"}" >/dev/null || true
set_storage_keys shelf operator@shelf.example "${EXAMPLE_SHELF_PASSWORD:-$dev_password}"
set_storage_keys adminui admin@adminui.example "${EXAMPLE_ADMINUI_PASSWORD:-$dev_password}"

