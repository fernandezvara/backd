#!/bin/sh
# Fails when a realm of the configuration has `auth: disabled`. Such a realm has no authentication
# at all and must never be exposed to the internet, and an installation on Kubernetes with a
# public address is exposed. Run it before building the image.
#
#   check-no-auth-disabled.sh [config directory]      default: config
#
# This is a guard, not a switch: if you are certain a realm without authentication belongs in
# this installation (it does not belong on the internet), remove the step that calls it from your
# workflow, on purpose and in a commit that says why.
set -eu

dir="${1:-config}"
[ -d "$dir" ] || { echo "check-no-auth-disabled: $dir is not a directory" >&2; exit 2; }

found=0
for f in "$dir"/*/realm.yaml; do
  [ -f "$f" ] || continue
  # A top-level `auth:` key (no indentation) whose value is disabled, with or without quotes
  # and with or without a trailing comment.
  if grep -Eq '^auth:[[:space:]]*["'"'"']?disabled["'"'"']?[[:space:]]*(#.*)?$' "$f"; then
    echo "check-no-auth-disabled: $f has 'auth: disabled'" >&2
    found=1
  fi
done

if [ "$found" -ne 0 ]; then
  cat >&2 <<'EOF'

A realm with `auth: disabled` has no authentication: anyone who can reach it can read and write
everything in it. This installation has a public address. Give the realm authentication, or keep
it out of this configuration. See Security -> Security model in the documentation.
EOF
  exit 1
fi
echo "check-no-auth-disabled: no realm without authentication in $dir"
