#!/usr/bin/env bash
# TLS certificates for the local stack (docker-compose.yml), managed with
# certsfor (https://www.certsfor.dev) running in a container. Everything it
# writes lives in docker/certs/ (gitignored):
#
#   docker/certs/ca.crt          the local CA: trust it to avoid warnings
#   docker/certs/localhost.crt   nginx's certificate (localhost, 127.0.0.1, ::1)
#   docker/certs/localhost.key   its private key
#   docker/certs/cfd/            certsfor's database (the CA and its key)
#   docker/certs/ca-id           the CA's id in that database
#
# The first run creates the CA and the certificate from the templates in
# docker/cfd/. Later runs ask certsfor for the certificate again, which
# renews it when less than 20% of its lifetime is left. `make example` runs
# this every time. Delete docker/certs/ to start over with a new CA.
set -euo pipefail
cd "$(dirname "$0")/.."

image=ghcr.io/fernandezvara/cfd:v0.2.5
dir=docker/certs
mkdir -p "$dir/cfd"

# Run certsfor as the current user, so the files belong to you. Rootless
# Podman needs keep-id for that.
user_args=(--user "$(id -u):$(id -g)")
if docker --version 2>/dev/null | grep -qi podman; then
  user_args+=(--userns=keep-id)
fi
ca_id=$(cat "$dir/ca-id" 2>/dev/null || true)
cfd() {
  docker run --rm "${user_args[@]}" -e HOME=/state -e CFD_CA_ID="$ca_id" \
    -v "$PWD/$dir/cfd:/state" -v "$PWD/$dir:/out" -v "$PWD/docker/cfd:/templates:ro" \
    "$image" "$@"
}

if [ -z "$ca_id" ]; then
  echo "Creating a local CA with certsfor…"
  ca_id=$(cfd create ca -f /templates/ca.yaml -q)
  echo "$ca_id" > "$dir/ca-id"
fi

if cfd list cert --csv | grep -q '^localhost,'; then
  request=(get cert --cn localhost)
else
  echo "Creating the localhost certificate…"
  request=(create cert -f /templates/localhost.yaml)
fi
# certsfor writes read-only files: write new ones, then replace the old.
rm -f "$dir"/.new.*
cfd "${request[@]}" -q -c /out/.new.crt -k /out/.new.key --ca-cert /out/.new.ca.crt
mv -f "$dir/.new.crt" "$dir/localhost.crt"
mv -f "$dir/.new.key" "$dir/localhost.key"
mv -f "$dir/.new.ca.crt" "$dir/ca.crt"
chmod 644 "$dir/ca.crt" "$dir/localhost.crt" # certificates aren't secret; the key stays 0400
