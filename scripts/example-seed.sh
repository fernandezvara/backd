#!/usr/bin/env bash
# Seeds the example realms of a running stack (make example, make dev-examples): the realms with
# files (shelf, adminui) keep their storage keys as secrets, set to the local MinIO's dev-only user,
# so uploads, previews and downloads work in the example apps.
#
# The work itself is scripts/seed.mjs, run with Deno inside the executor container (the executor
# image is the Deno one): it signs accounts up and in — consents, TOTP enrollment, a second
# factor — with the repository's own JS client, and prints the credentials document on stdout,
# which this writes to $SEED_DIR/credentials.json (gitignored, served by nginx at /local-seed/
# and rendered by the welcome page at /). The one thing only the API host can do stays here:
# `backd bootstrap`, the local command that creates the first administrator of a realm.
#
# Environment: ORIGIN (https://localhost:8443), SEED_DIR (./local-seed),
# BACKD_EXEC — the command that runs the backd binary in the stack's API container — and
# EXEC_EXEC — the command that runs a command in the executor container. E.g.:
#   BACKD_EXEC="docker compose exec -T backd /backd" EXEC_EXEC="docker compose exec -T executor"
set -euo pipefail
cd "$(dirname "$0")/.."
origin=${ORIGIN:-https://localhost:8443}
backd_exec=${BACKD_EXEC:?set BACKD_EXEC to the command that runs backd in the API container}
exec_exec=${EXEC_EXEC:?set EXEC_EXEC to the command that runs a command in the executor container}
seed_dir=${SEED_DIR:-local-seed}
mkdir -p "$seed_dir"

dev_password='dev-p4ssw0rd!'

# compose's exec does not forward the environment: every exported EXAMPLE_* the
# caller set (a realm's password override) is passed through `env`.
mapfile -t example_env < <(compgen -e EXAMPLE_ || true)

# The realms are the directories of the config the stack runs (CONFIG_DIR, mounted
# at /seed-config in the executor): seed.mjs reads each realm.yaml for what to seed.
# `plan` names the seeded full administrator of each realm signup can't create one
# on (invite, closed): bootstrap them here — it is a local command of the API host,
# and idempotent: an existing administrator keeps its password.
while IFS=$'\t' read -r realm email; do
  [ -n "$realm" ] || continue
  var=EXAMPLE_$(printf '%s' "$realm" | tr 'a-z-' 'A-Z_')_PASSWORD
  printf '%s\n' "${!var:-$dev_password}" | $backd_exec bootstrap --realm "$realm" --email "$email" >/dev/null 2>&1 || true
done < <($exec_exec env "${example_env[@]}" deno run --allow-all /seed/seed.mjs plan)

# Everything else — signups, the consent/MFA chain, the storage keys — is the Deno script.
# (A temp file, then the rename: the script reads credentials.json for a rerun's TOTP
# secret, and `>` would empty it before it started.)
tmp=$seed_dir/.credentials.json.tmp
$exec_exec env "${example_env[@]}" deno run --allow-all /seed/seed.mjs > "$tmp"
mv "$tmp" "$seed_dir/credentials.json"

echo "  Sign-in details (QR, recovery codes): $seed_dir/credentials.json — also rendered at $origin/"
