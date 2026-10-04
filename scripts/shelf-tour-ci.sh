#!/usr/bin/env bash
# Builds the tutorial's starter folder from the repository, with backd and the
# executor built from this checkout instead of the released images, brings the
# stack up exactly as the Tutorial tells the reader to, and replays the whole
# tutorial against it (clients/js/examples/shelf/tour.js). Prints the logs when
# something fails and always stops the stack.
#
#   scripts/shelf-tour-ci.sh         # or: make shelf-tour-ci
#
# Needs docker and node, and port 8080 free (the tutorial's own port).
set -euo pipefail
cd "$(dirname "$0")/.."
root=$PWD

command -v docker >/dev/null || { echo "needs 'docker' on your PATH" >&2; exit 1; }
command -v node >/dev/null || { echo "needs 'node' on your PATH" >&2; exit 1; }

export COMPOSE_PROJECT_NAME=${COMPOSE_PROJECT_NAME:-shelf-tour}
dir=$(mktemp -d)
compose=$dir/compose.yaml

cleanup() {
  trap - EXIT
  docker compose -f "$compose" down -v >/dev/null 2>&1 || true
  rm -rf "$dir"
}
trap cleanup EXIT

docker build -t backd:shelf-tour .
docker build -f docker/executor/Dockerfile -t backd-executor:shelf-tour .

# The starter folder, as the Tutorial's "The starter" section builds it.
mkdir -p "$dir/app/lib" "$dir/client" "$dir/config"
cp docs/static/tutorial/nginx.conf "$dir/"
sed -e 's#ghcr.io/fernandezvara/backd-executor:[A-Za-z0-9._-]*#backd-executor:shelf-tour#' \
    -e 's#ghcr.io/fernandezvara/backd:[A-Za-z0-9._-]*#backd:shelf-tour#' \
    docs/static/tutorial/compose.yaml > "$compose"
cp clients/js/examples/shelf/{index.html,app.js,style.css} "$dir/app/"
cp clients/js/examples/shelf/lib/* "$dir/app/lib/"
cp clients/js/src/*.js "$dir/client/"
cp -r examples/config/shelf "$dir/config/shelf"
find "$dir/config" -name .build -prune -exec rm -rf {} +

docker compose -f "$compose" up -d

ready() { curl -sf http://localhost:8080/readyz >/dev/null; }
for _ in $(seq 1 120); do
  ready && break
  sleep 2
done
ready || {
  echo "the tutorial stack didn't become ready" >&2
  docker compose -f "$compose" logs --tail 80 >&2
  exit 1
}

# The tour bootstraps the operator through `docker compose exec` on this stack.
if ! SHELF_COMPOSE=$compose BACKD_URL=http://localhost:8080 node "$root/clients/js/examples/shelf/tour.js"; then
  docker compose -f "$compose" logs --tail 120 backd >&2
  exit 1
fi
