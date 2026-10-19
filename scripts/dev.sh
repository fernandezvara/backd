#!/usr/bin/env bash
# The development stack (docker-compose.dev.yml): released backd images over your own config,
# on https://localhost:8443 with the local CA of `make example` (docker/certs/).
#
#   scripts/dev.sh build   pull the images, make the certificates and a starter ./config
#   scripts/dev.sh up      start the stack (nothing is pulled or built) and follow the logs
#   scripts/dev.sh down    stop it, keeping the data
#   scripts/dev.sh reset   stop it and delete the data (mongo, minio, prometheus, grafana)
#   scripts/dev.sh logs    follow the logs; S=worker for one service
#   scripts/dev.sh backd … run the backd command line in the API container (make dev-backd ARGS='…')
#
# Settings, from the environment:
#   CONFIG_DIR     your config (default ./config), mounted read-write: backd follows it
#   BACKD_VERSION  the image tag (default latest); `local` uses images you built yourself
#   WORKERS        how many workers (default 1)
#   LOG_LEVEL      info by default
#   EXAMPLES=1     run the example realms of the repository (examples/config) and their apps, seeded
#                  with the storage keys and administrators they need (`make dev-examples`)
set -euo pipefail
cd "$(dirname "$0")/.."

for cmd in docker curl; do
  command -v "$cmd" >/dev/null || { echo "make dev needs '$cmd' on your PATH" >&2; exit 1; }
done

# EXAMPLES=1 runs the repository's example realms (examples/config) and seeds them.
if [ "${EXAMPLES:-}" = 1 ]; then export CONFIG_DIR="${CONFIG_DIR:-./examples/config}"; fi
export CONFIG_DIR="${CONFIG_DIR:-./config}"
export BACKD_VERSION="${BACKD_VERSION:-latest}"
export DEV_UID="${DEV_UID:-$(id -u)}" DEV_GID="${DEV_GID:-$(id -g)}"
# Rootless Podman maps root in the container to you on the host: running as root there is what
# makes the files backd writes into your config yours.
if docker --version 2>/dev/null | grep -qi podman && [ "$(id -u)" != 0 ]; then export DEV_UID=0 DEV_GID=0; fi
# Images you built yourself (BACKD_VERSION=local) are not pulled.
if [ "$BACKD_VERSION" = local ]; then export BACKD_PULL=never; else export BACKD_PULL=always; fi
compose=(docker compose -f docker-compose.dev.yml)
ca=docker/certs/ca.crt
origin=https://localhost:8443
image="ghcr.io/fernandezvara/backd-executor:$BACKD_VERSION"
seed_dir=local-seed

# The welcome page (/) reads local-seed/stack.json for its links and
# local-seed/credentials.json for the seeded accounts (written by
# example-seed.sh when EXAMPLES=1). Gitignored, localhost only.
write_stack_json() {
  mkdir -p "$seed_dir"
  local example_link=''
  [ "${EXAMPLES:-}" = 1 ] && example_link='{"href":"/example/","label":"/example/","note":"the example apps: blog, expenses, shelf, the workshop, the mailbox"},'
  cat > "$seed_dir/stack.json" <<EOF
{
  "title": "backd dev stack",
  "lead": "Released images over your own config ($CONFIG_DIR, mounted read-write: save a file and backd reloads it).",
  "sections": [
    {"title": "Your backd", "links": [
      {"href": "/readyz", "label": "/readyz", "note": "the API; try /v1/{realm}/…"},
      {"href": "/_ui/", "label": "/_ui/", "note": "the admin web interface"},
      $example_link
      {"href": "https://fernandezvara.github.io/backd/", "label": "Documentation", "note": "online"}
    ]},
    {"title": "Observe", "links": [
      {"href": "http://localhost:3000/", "label": "Grafana", "note": "backd's dashboards"},
      {"href": "http://localhost:9090/targets", "label": "Prometheus targets", "note": "backd, worker, executor and egress"},
      {"href": "http://localhost:9001/", "label": "MinIO console", "note": "backd-dev / dev-p4ssw0rd!"}
    ]}
  ],
  "credentials_hint": "No seeded accounts. Create the first administrator of a realm with: make dev-backd ARGS='bootstrap --realm <realm> --email you@example.com'"
}
EOF
}

# A starter config when there is none: a realm "app" with the sample blog database. It is
# yours to change; backd follows it.
starter() {
  if [ -d "$CONFIG_DIR" ] && [ -n "$(ls -A "$CONFIG_DIR" 2>/dev/null)" ]; then return 0; fi
  echo "Creating a starter config in $CONFIG_DIR (a realm \"app\" with a sample database)…"
  mkdir -p "$CONFIG_DIR"
  docker run --rm --user "$DEV_UID:$DEV_GID" -e CONFIG_DIR=/config -e HOME=/tmp \
    -v "$PWD/$CONFIG_DIR:/config" --pull "${BACKD_PULL/always/missing}" --entrypoint /usr/local/bin/backd "$image" \
    template realm --realm app --sample >/dev/null
}

# Stops the stack: compose's own down, then whatever of the project is left (podman-compose
# only knows the workers a plain `down` has in its file, not the extra ones WORKERS started).
stop_all() { # [-v]
  "${compose[@]}" down "$@" >/dev/null 2>&1 || true
  # Podman refuses to remove a container others depend on, so go round until none is left; its
  # banner on standard output is why only ids are kept.
  for _ in 1 2 3; do
    left=$(docker ps -aq --filter "label=com.docker.compose.project=backd-dev" 2>/dev/null | grep -E '^[0-9a-f]{12,64}$' || true)
    [ -n "$left" ] || break
    for id in $left; do docker rm -f "$id" >/dev/null 2>&1 || true; done
  done
  if [ "${1:-}" = -v ]; then "${compose[@]}" down -v >/dev/null 2>&1 || true; fi
}

case "${1:-up}" in
  build)
    scripts/local-certs.sh
    starter
    if [ "$BACKD_PULL" = never ]; then
      echo "BACKD_VERSION=local: the backd images are the ones you built; pulling the others…"
      "${compose[@]}" pull mongo minio minio-setup prometheus grafana nginx
    else
      echo "Pulling the images ($image and the stack's)…"
      "${compose[@]}" pull
    fi
    echo
    echo "Ready. Start the stack with: make dev"
    ;;

  down)
    stop_all
    echo "Stopped (the data stays; make dev-reset wipes it)."
    ;;

  reset)
    stop_all -v
    echo "The data is gone (your config in $CONFIG_DIR is untouched)."
    ;;

  backd)
    shift
    tty=(); [ -t 0 ] || tty=(-T)   # a terminal for prompts (passwords), none when input is piped
    exec "${compose[@]}" exec "${tty[@]}" backd /usr/local/bin/backd "$@"
    ;;

  logs)
    exec "${compose[@]}" logs -f --tail 100 ${S:-}
    ;;

  up)
    [ -f docker/certs/localhost.crt ] || { echo "No certificates yet: run 'make dev-build' first." >&2; exit 1; }
    if [ ! -d "$CONFIG_DIR" ] || [ -z "$(ls -A "$CONFIG_DIR" 2>/dev/null)" ]; then
      echo "$CONFIG_DIR is missing or empty: run 'make dev-build' to create a starter config, or set CONFIG_DIR." >&2
      exit 1
    fi
    docker image inspect "$image" >/dev/null 2>&1 || { echo "The image $image is not here yet: run 'make dev-build' first." >&2; exit 1; }

    follower=""
    cleanup() {
      trap - EXIT INT TERM
      echo
      echo "Stopping… (the data stays; make dev-reset wipes it)"
      if [ -n "$follower" ]; then
        pkill -P "$follower" 2>/dev/null || true
        kill "$follower" 2>/dev/null || true
      fi
      stop_all
      echo "Stopped."
    }
    trap cleanup EXIT
    trap 'cleanup; exit 0' INT TERM

    scripts/local-certs.sh   # renews the certificate when it is about to expire; the CA stays
    write_stack_json         # before nginx mounts $seed_dir, so it is yours, not root's
    echo "Starting the stack from $image (nothing is built)…"
    # backd refuses stale or missing bundles: build them first, and wait for it (not every
    # compose implementation waits for a one-shot service).
    "${compose[@]}" run --rm functions-build
    "${compose[@]}" up -d --scale "worker=${WORKERS:-1}"

    ready() { curl -sf --cacert "$ca" "$origin/readyz" >/dev/null; }
    for _ in $(seq 1 180); do ready && break; sleep 1; done
    ready || { "${compose[@]}" logs --tail 60 >&2; exit 1; }

    examples=""
    if [ "${EXAMPLES:-}" = 1 ]; then
      echo "Seeding the example realms…"
      ORIGIN=$origin CA=$ca SEED_DIR=$seed_dir \
        BACKD_EXEC="${compose[*]} exec -T backd /usr/local/bin/backd" \
        EXEC_EXEC="${compose[*]} exec -T executor" \
        scripts/example-seed.sh
      examples="  $origin/example/     the example apps: blog, expenses, shelf, the workshop, the mailbox (operator@shelf.example and
                       admin@adminui.example have the password dev-p4ssw0rd!)
"
    fi
    if curl -sf "$origin/readyz" >/dev/null 2>&1; then
      trust="The local CA ($ca) is trusted by this system."
    else
      trust="Trust the local CA once ($ca): see docs → Getting started → HTTPS with a local CA."
    fi
    cat <<MSG

  $origin/             the welcome page: links, and the seeded accounts' sign-in details
                       (passwords, TOTP QR and recovery codes — from ./local-seed/, gitignored)
  $origin/v1/…         the API, with the realms in $CONFIG_DIR
  $origin/_ui/         the admin web interface
$examples  http://localhost:3000/ Grafana    http://localhost:9090/ Prometheus    http://localhost:9001/ MinIO console
  Docs: https://fernandezvara.github.io/backd/

  $trust

  Your config is $CONFIG_DIR, mounted read-write: save a file and backd (and the worker) reload it;
  functions are bundled again and backd.d.ts is written for your editor. See "Development" in the docs.
  Workers: ${WORKERS:-1}. make dev-logs S=worker follows the worker alone. Ctrl-C stops the stack.

MSG
    ( while :; do "${compose[@]}" logs -f --since 1s 2>/dev/null || true; sleep 1; done ) &
    follower=$!
    while :; do sleep 3600 & wait $! || true; done
    ;;

  *)
    echo "usage: scripts/dev.sh build|up|down|reset|logs|backd" >&2
    exit 2
    ;;
esac
