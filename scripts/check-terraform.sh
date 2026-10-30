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

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for d in $dirs; do
  echo "== $d"
  terraform -chdir="$d" fmt -check -recursive -diff
  # Checked in a clean copy: a terraform.tfvars, a backend.hcl, a state or a .terraform directory of
  # your own (this is the directory you run Terraform in) must not change what the tests see.
  copy="$work/$(echo "$d" | tr '/' '_')"
  mkdir -p "$copy"
  tar -C "$d" --exclude='.terraform' --exclude='*.tfvars' --exclude='*.tfvars.json' --exclude='backend.hcl' --exclude='*.tfstate' --exclude='*.tfstate.*' -cf - . | tar -C "$copy" -xf -
  terraform -chdir="$copy" init -backend=false -input=false -no-color >/dev/null
  terraform -chdir="$copy" validate -no-color
  if ls "$copy"/tests/*.tftest.hcl >/dev/null 2>&1; then
    terraform -chdir="$copy" test -no-color
  fi
done
