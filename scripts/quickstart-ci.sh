#!/usr/bin/env bash
# Runs the Quickstart's commands, as written on the page, against the RELEASED images: the project
# `backd template project` writes (built from this checkout) starts on the images of the latest
# release, and a first document is made through it. It is the test that the path a newcomer
# follows works, which nothing else runs: the tutorial, the production reference and the example
# stack are tested, the template project is this.
#
#   scripts/quickstart-ci.sh        needs docker compose, curl, go and python3
#
# The commands come from scripts/quickstart-script.py (the block of the page between the
# quickstart markers); the three that ask for a password are given one on standard input.
set -euo pipefail
cd "$(dirname "$0")/.."
root=$PWD
work=$(mktemp -d)
export HOME=$work/home
mkdir -p "$HOME" "$work/bin"
export GOFLAGS=-modcacherw   # the module cache under $HOME can then be removed with the rest
export QUICKSTART_PASSWORD='dev-p4ssw0rd!1'
. "$root/scripts/retry.sh"

cleanup() {
  status=$?
  if [ -d "$work/my-app" ]; then
    cd "$work/my-app"
    if [ "$status" -ne 0 ]; then
      echo "--- the Quickstart failed; the end of the stack's logs:"
      docker compose logs --tail 60 2>&1 || true
    fi
    docker compose down -v >/dev/null 2>&1 || true
  fi
  # The containers write root-owned files into the project (the README tells Docker users to set
  # DEV_UID for that): they cannot be removed here, and the runner is thrown away anyway.
  rm -rf "$work" 2>/dev/null || true
  exit "$status"
}
trap cleanup EXIT

go build -o "$work/bin/backd" ./cmd/backd
export PATH="$work/bin:$PATH"

# The images are pulled on their own so that a registry failure is retried and the commands
# below are not (they are not idempotent).
for image in ghcr.io/fernandezvara/backd-executor:latest docker.io/library/mongo:8.2 cgr.dev/chainguard/minio:latest; do
  retry docker pull "$image"
done

scripts/quickstart-script.py > "$work/quickstart.sh"
cat >> "$work/quickstart.sh" <<'CHECK'

# The first document is there: the POST above was accepted, and the key reads it back.
echo
listing=$(curl -fsS http://127.0.0.1:8080/v1/demo/main/posts -H "Authorization: Bearer $KEY")
echo "$listing" | grep -q '"title":"Hello"' || { echo "the document made by the Quickstart is not in the list: $listing"; exit 1; }
echo "the Quickstart works: the first document is in the list"
CHECK
echo "--- the Quickstart's commands"
cat "$work/quickstart.sh"
echo "---"
cd "$work"
timeout 900 bash -euo pipefail -x quickstart.sh
