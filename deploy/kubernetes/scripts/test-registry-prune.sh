#!/bin/sh
# Tests registry-prune.sh with a fake doctl that records its calls.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/doctl" <<'EOF'
#!/bin/sh
echo "$*" >> "$DOCTL_LOG"
case "$*" in
  "registry repository list-tags "*)
    printf 'r1 2026-01-01T00:00:00Z\nr5 2026-05-01T00:00:00Z\nr3 2026-03-01T00:00:00Z\nr2 2026-02-01T00:00:00Z\nr4 2026-04-01T00:00:00Z\n' ;;
esac
EOF
chmod +x "$tmp/doctl"
export PATH="$tmp:$PATH" DOCTL_LOG="$tmp/log"

fail() { echo "FAIL: $1" >&2; echo "--- calls:" >&2; cat "$DOCTL_LOG" >&2; exit 1; }

# keeps the newest 2 (r5, r4), deletes r3 r2 r1, then collects garbage
: > "$DOCTL_LOG"
sh "$here/registry-prune.sh" acme 2 > "$tmp/out"
grep -q "^registry repository delete-tag acme r3 --force$" "$DOCTL_LOG" || fail "r3 not deleted"
grep -q "^registry repository delete-tag acme r2 --force$" "$DOCTL_LOG" || fail "r2 not deleted"
grep -q "^registry repository delete-tag acme r1 --force$" "$DOCTL_LOG" || fail "r1 not deleted"
! grep -q "delete-tag acme r[45] " "$DOCTL_LOG" || fail "a newest tag was deleted"
grep -q "^registry garbage-collection start --include-untagged-manifests --force$" "$DOCTL_LOG" || fail "no garbage collection"

# KEEP_TAGS protects an old tag
: > "$DOCTL_LOG"
KEEP_TAGS="r1" sh "$here/registry-prune.sh" acme 2 > "$tmp/out"
! grep -q "delete-tag acme r1 " "$DOCTL_LOG" || fail "r1 was deleted despite KEEP_TAGS"
grep -q "delete-tag acme r2 " "$DOCTL_LOG" || fail "r2 not deleted"

# a dry run deletes nothing and collects nothing
: > "$DOCTL_LOG"
DRY_RUN=1 sh "$here/registry-prune.sh" acme 2 > "$tmp/out"
! grep -q "delete-tag\|garbage" "$DOCTL_LOG" || fail "a dry run changed something"
grep -q "would delete  acme:r3" "$tmp/out" || fail "a dry run did not list r3"

# nothing to prune: no garbage collection
: > "$DOCTL_LOG"
sh "$here/registry-prune.sh" acme 10 > "$tmp/out"
! grep -q "delete-tag\|garbage" "$DOCTL_LOG" || fail "pruned with fewer tags than keep"

# bad arguments
sh "$here/registry-prune.sh" acme zero >/dev/null 2>&1 && fail "accepted a non-numeric keep"
sh "$here/registry-prune.sh" acme 0 >/dev/null 2>&1 && fail "accepted keep 0"
sh "$here/registry-prune.sh" >/dev/null 2>&1 && fail "accepted no repository"

echo "registry-prune: ok"
