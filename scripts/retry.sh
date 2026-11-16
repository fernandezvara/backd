#!/bin/sh
# Runs a command again when it fails because of the infrastructure, not because of the code.
#
#   scripts/retry.sh sudo apt-get update            run it as a script
#   . scripts/retry.sh; retry compose up -d --build  or source it and call retry (works with shell functions)
#
# A failure is the infrastructure's when what the command printed matches INFRA_PATTERN (a package
# mirror, the Go module proxy, an image registry, a dropped connection). Anything else is the
# code's and fails at once, so a real failure is never run three times and never hidden. At most
# RETRY_ATTEMPTS runs (default 3), RETRY_PAUSE seconds apart (default 15, doubling). Each retry
# prints a GitHub Actions warning titled "infrastructure retry", so the retries can be counted
# (scripts/ci-failures.py does).
INFRA_PATTERN='Failed to fetch|Unable to fetch some archives|Temporary failure|Could not resolve|Connection (timed out|reset|refused)|connection reset|TLS handshake timeout|i/o timeout|unexpected EOF|toomanyrequests|502 Bad Gateway|503 Service|504 Gateway|Hash Sum mismatch|proxy\.golang\.org.*(timeout|EOF|reset|unexpected)|read "https://proxy\.golang\.org|received unexpected HTTP status: 5|error pulling image|failed to copy: httpReadSeeker|Installation process exited with code: 100'

retry() {
  _attempts=${RETRY_ATTEMPTS:-3}
  _pause=${RETRY_PAUSE:-15}
  _n=1
  _log=$(mktemp)
  while :; do
    "$@" >"$_log" 2>&1
    _status=$?
    cat "$_log"
    if [ "$_status" -eq 0 ]; then
      rm -f "$_log"
      return 0
    fi
    if [ "$_n" -ge "$_attempts" ] || ! grep -Eq "$INFRA_PATTERN" "$_log"; then
      rm -f "$_log"
      return "$_status"
    fi
    _why=$(grep -E "$INFRA_PATTERN" "$_log" | head -n 1 | cut -c1-160)
    echo "::warning title=infrastructure retry::attempt $_n of $_attempts of '$1 $2' failed (exit $_status): $_why"
    sleep "$_pause"
    _pause=$((_pause * 2))
    _n=$((_n + 1))
  done
}

case "$0" in
  *retry.sh) retry "$@" ;;
esac
