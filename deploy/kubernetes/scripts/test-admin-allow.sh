#!/bin/sh
# Tests admin-allow.sh with a fake kubectl that records its calls.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/kubectl" <<'EOF'
#!/bin/sh
echo "$*" >> "$KUBECTL_LOG"
if [ "$*" = "create configmap backd-admin-allow" ]; then :; fi
# `apply -f -` reads the manifest from stdin
case "$*" in *"apply -f -"*) cat >> "$KUBECTL_LOG" ;; esac
case "$*" in *"create configmap"*) printf 'kind: ConfigMap\n' ;; esac
EOF
chmod +x "$tmp/kubectl"
export KUBECTL="$tmp/kubectl" KUBECTL_LOG="$tmp/log"

fail() { echo "FAIL: $1" >&2; cat "$KUBECTL_LOG" >&2; exit 1; }

: > "$KUBECTL_LOG"
NAMESPACE=acme sh "$here/admin-allow.sh" "203.0.113.7/32,2001:db8::/48" >/dev/null
grep -q 'patch middleware.traefik.io backd-admin-allow' "$KUBECTL_LOG" || fail "the middleware was not patched"
grep -q '"203.0.113.7/32","2001:db8::/48"' "$KUBECTL_LOG" || fail "the list is not in the patch"
grep -q -- '-n acme' "$KUBECTL_LOG" || fail "NAMESPACE was not used"
grep -q 'configmap backd-admin-allow --from-literal=allowedCIDRs=203.0.113.7/32,2001:db8::/48' "$KUBECTL_LOG" || fail "the ConfigMap does not keep the list"

for bad in 0.0.0.0/0 ::/0 10.0.0.0/0 203.0.113.7 'foo/32' '203.0.113.7/32;rm' ''; do
  : > "$KUBECTL_LOG"
  sh "$here/admin-allow.sh" "$bad" >/dev/null 2>&1 && fail "accepted '$bad'"
  [ ! -s "$KUBECTL_LOG" ] || fail "changed the cluster for '$bad'"
done

# one bad entry in a list stops everything
: > "$KUBECTL_LOG"
sh "$here/admin-allow.sh" "203.0.113.7/32,0.0.0.0/0" >/dev/null 2>&1 && fail "accepted a list with 0.0.0.0/0"
[ ! -s "$KUBECTL_LOG" ] || fail "changed the cluster for a bad list"

echo "admin-allow: ok"
