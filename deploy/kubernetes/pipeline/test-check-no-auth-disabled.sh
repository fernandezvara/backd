#!/bin/sh
# Tests check-no-auth-disabled.sh on small configuration trees.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

fail() { echo "FAIL: $1" >&2; exit 1; }
realm() { mkdir -p "$tmp/$1/$2"; printf '%s\n' "$3" > "$tmp/$1/$2/realm.yaml"; }

realm ok shop "auth: enabled"
realm ok blog "name: blog"
sh "$here/check-no-auth-disabled.sh" "$tmp/ok" >/dev/null || fail "refused a configuration whose realms authenticate"

for v in 'auth: disabled' 'auth:   disabled' 'auth: "disabled"' "auth: 'disabled'" 'auth: disabled # local only'; do
  rm -rf "$tmp/bad"
  realm bad open "$v"
  realm bad shop "auth: enabled"
  sh "$here/check-no-auth-disabled.sh" "$tmp/bad" >/dev/null 2>&1 && fail "accepted: $v"
done

# a nested key, a comment and another word are not the setting
rm -rf "$tmp/fine"
realm fine a "# auth: disabled"
realm fine b "settings:
  auth: disabled"
realm fine c "auth: disabled-soon"
sh "$here/check-no-auth-disabled.sh" "$tmp/fine" >/dev/null 2>&1 || fail "refused a comment, a nested key and another word"

sh "$here/check-no-auth-disabled.sh" "$tmp/missing" >/dev/null 2>&1 && fail "accepted a missing directory"

# the repository's own examples: shop has no authentication, blog has
sh "$here/check-no-auth-disabled.sh" "$here/../../../examples/config" >/dev/null 2>&1 && fail "accepted the examples, whose shop realm has auth: disabled"

echo "check-no-auth-disabled: ok"
