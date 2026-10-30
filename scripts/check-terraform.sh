#!/bin/sh
# Checks the Terraform configurations of deploy/terraform/<provider> without an account: format,
# validate against no backend, and `terraform test` (the providers are mocked, so the plans run
# offline). Usage: scripts/check-terraform.sh [directory...]
set -eu
cd "$(dirname "$0")/.."

if [ "$#" -gt 0 ]; then
  dirs="$*"
else
  dirs="deploy/terraform/digitalocean/bootstrap deploy/terraform/digitalocean"
fi

for d in $dirs; do
  echo "== $d"
  terraform -chdir="$d" fmt -check -recursive -diff
  terraform -chdir="$d" init -backend=false -input=false -no-color >/dev/null
  terraform -chdir="$d" validate -no-color
  if ls "$d"/tests/*.tftest.hcl >/dev/null 2>&1; then
    terraform -chdir="$d" test -no-color
  fi
done
