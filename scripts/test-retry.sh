#!/bin/sh
# Tests scripts/retry.sh: it runs again only for infrastructure failures, and stops.
set -u
cd "$(dirname "$0")" || exit 1
export RETRY_PAUSE=0
fails=0
check() { if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: got '$2', want '$3'"; fails=$((fails + 1)); fi; }
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT

# A command that fails <n> times with <message>, then succeeds; counts its runs.
flaky() { # flaky <n> <message>
  c=$(cat "$dir/count" 2>/dev/null || echo 0); c=$((c + 1)); echo "$c" >"$dir/count"
  if [ "$c" -le "$1" ]; then echo "$2" >&2; return 1; fi
  echo fine
}
runs() { cat "$dir/count" 2>/dev/null || echo 0; }
reset() { rm -f "$dir/count"; }

. ./retry.sh

reset; retry flaky 0 "" >/dev/null; check "success runs once" "$(runs)" 1
reset; retry flaky 2 "E: Failed to fetch http://archive.ubuntu.com/x" >/dev/null 2>&1; check "an apt failure is retried until it works" "$(runs)" 3
reset; retry flaky 9 "E: Failed to fetch http://archive.ubuntu.com/x" >/dev/null 2>&1; check "it gives up after 3 runs" "$(runs)" 3
reset; retry flaky 9 "main.go:3: undefined: foo" >/dev/null 2>&1; check "a failure of the code is not retried" "$(runs)" 1
reset; retry flaky 1 'read "https://proxy.golang.org/x/@v/v1.zip": unexpected EOF' >/dev/null 2>&1; check "the Go module proxy is retried" "$(runs)" 2
reset; retry flaky 1 "toomanyrequests: You have reached your pull rate limit" >/dev/null 2>&1; check "a registry limit is retried" "$(runs)" 2
reset; RETRY_ATTEMPTS=2 retry flaky 9 "Connection reset by peer" >/dev/null 2>&1; check "RETRY_ATTEMPTS sets the limit" "$(runs)" 2
reset; retry flaky 9 "main.go:3: undefined: foo" >/dev/null 2>&1; check "the exit status of the code's failure is kept" "$?" 1
reset; out=$(retry flaky 1 "502 Bad Gateway" 2>&1); case "$out" in *"::warning title=infrastructure retry::"*) r=yes ;; *) r=no ;; esac; check "a retry is announced" "$r" yes
reset; sh ./retry.sh sh -c 'echo "E: Failed to fetch x" >&2; exit 3' >/dev/null 2>&1; check "it runs as a script and keeps the status" "$?" 3

[ "$fails" -eq 0 ] || { echo "$fails check(s) failed"; exit 1; }
