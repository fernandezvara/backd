#!/bin/sh
# Changes who may reach the admin host, in place and without a release.
#
#   admin-allow.sh <cidr>[,<cidr>...]    allow exactly these addresses
#   admin-allow.sh --show                print what is allowed now
#
#   admin-allow.sh "$(curl -s https://api.ipify.org)/32"    allow the address you are on
#
# It patches the Traefik Middleware `backd-admin-allow` and keeps the same list in the ConfigMap
# `backd-admin-allow`, which the chart reads on every `helm upgrade` (it wins over the values, so a
# release does not undo your change). NAMESPACE (default backd) and KUBECTL (default kubectl) say
# where and how.
#
# This only concerns HTTP to the admin host. If your address changed and you are locked out, you
# are not locked out of the cluster: run this with your cluster credentials, or reach the admin
# instance directly with `kubectl port-forward svc/backd-admin 8081:8080 -n backd`.
set -eu

namespace="${NAMESPACE:-backd}"
kubectl="${KUBECTL:-kubectl}"
cm=backd-admin-allow
mw=backd-admin-allow

if [ "${1:-}" = "--show" ]; then
  $kubectl -n "$namespace" get middleware.traefik.io "$mw" -o jsonpath='{.spec.ipAllowList.sourceRange}'
  echo
  exit 0
fi

list="${1:-}"
[ -n "$list" ] || { echo "usage: admin-allow.sh <cidr>[,<cidr>...] | --show" >&2; exit 2; }

json=""
for c in $(echo "$list" | tr ',' ' '); do
  # An IPv4 or IPv6 address with a prefix length. No prefix is refused: say /32 or /128 on purpose.
  case "$c" in
    */*) ;;
    *) echo "admin-allow: '$c' has no prefix length (use /32 for one IPv4 address, /128 for one IPv6)" >&2; exit 2 ;;
  esac
  case "$c" in
    0.0.0.0/0 | ::/0 | */0)
      echo "admin-allow: '$c' would open the admin host to everyone, and the admin host is never opened to everyone" >&2; exit 2 ;;
  esac
  case "$c" in
    *[!0-9a-fA-F:./]*) echo "admin-allow: '$c' is not an address range" >&2; exit 2 ;;
  esac
  json="${json:+$json,}\"$c\""
done

$kubectl -n "$namespace" patch middleware.traefik.io "$mw" --type merge \
  -p "{\"spec\":{\"ipAllowList\":{\"sourceRange\":[$json]}}}"
$kubectl -n "$namespace" create configmap "$cm" --from-literal=allowedCIDRs="$list" \
  --dry-run=client -o yaml | $kubectl -n "$namespace" apply -f -
echo "the admin host now allows: $list"
