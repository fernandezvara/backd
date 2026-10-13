#!/bin/sh
# Generates each example realm's OpenAPI document and builds Dart, Kotlin and Swift clients
# from it with openapi-generator, so the document stays usable by real generators. Needs Go
# and docker (or podman via DOCKER=podman). Output goes to a temporary directory.
set -eu

DOCKER=${DOCKER:-docker}
GENERATOR=${GENERATOR:-docker.io/openapitools/openapi-generator-cli:v7.10.0}
REDOCLY=${REDOCLY:-@redocly/cli@2}
root=$(cd "$(dirname "$0")/.." && pwd)
work=$(mktemp -d "${TMPDIR:-/tmp}/backd-openapi.XXXXXX")
trap 'rm -rf "$work"' EXIT

cd "$root"
go build -o "$work/backd" ./cmd/backd

for realm in shelf workshop; do
  echo "== $realm"
  CONFIG_DIR="$root/examples/config" "$work/backd" openapi --realm "$realm" --out "$work/$realm.json" >/dev/null
  npx --yes "$REDOCLY" lint --config "$root/api/redocly.yaml" "$work/$realm.json"
  # The admin document, for internal tooling: it lints, and a generator reads it.
  CONFIG_DIR="$root/examples/config" "$work/backd" openapi --realm "$realm" --admin --out "$work/$realm-admin.json" >/dev/null
  npx --yes "$REDOCLY" lint --config "$root/api/redocly.yaml" "$work/$realm-admin.json"
  "$DOCKER" run --rm -v "$work:/local" "$GENERATOR" generate \
    -i "/local/$realm-admin.json" -g kotlin -o "/local/$realm-admin-kotlin" >"$work/$realm-admin.log" 2>&1 \
    || { cat "$work/$realm-admin.log"; echo "openapi-generator failed for the $realm admin document" >&2; exit 1; }
  for gen in dart kotlin swift5; do
    out="$work/$realm-$gen"
    "$DOCKER" run --rm -v "$work:/local" "$GENERATOR" generate \
      -i "/local/$realm.json" -g "$gen" -o "/local/$realm-$gen" >"$work/$realm-$gen.log" 2>&1 \
      || { cat "$work/$realm-$gen.log"; echo "openapi-generator failed for $realm / $gen" >&2; exit 1; }
    files=$(find "$out" -type f | wc -l)
    [ "$files" -gt 20 ] || { echo "$realm / $gen produced only $files files" >&2; exit 1; }
    echo "   $gen: $files files"
  done
done
