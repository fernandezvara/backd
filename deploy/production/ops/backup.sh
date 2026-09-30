#!/bin/bash
# Encrypted backup of every collection the config uses: the realms' data
# databases and their <realm>___system databases (users, password hashes,
# sessions, API keys). Run as the "backup" user that provision.sh creates:
# its role can read exactly those collections, so it defines what the dump
# contains.
#
#   mongodump --archive --gzip  |  age -r <recipient>...  >  /backups/backd-<UTC time>.archive.gz.age
#
# Needs BACKUP_RECIPIENTS: one or more age public keys (age1…), separated
# by spaces. Only the matching private keys can decrypt, so this host never
# holds the means to read its own backups. It never writes an unencrypted
# backup. Keeps the newest BACKUP_KEEP backups (default 14) in /backups.
set -euo pipefail
: "${MONGO_URI:?}"
if [ -z "${BACKUP_RECIPIENTS:-}" ]; then
  echo "backup: BACKUP_RECIPIENTS is empty; set it to your age public key(s) (age1...)" >&2
  exit 1
fi
keep=${BACKUP_KEEP:-14}

recipients=()
for r in $BACKUP_RECIPIENTS; do recipients+=(-r "$r"); done

name=backd-$(date -u +%Y%m%dT%H%M%SZ).archive.gz.age
tmp=/backups/.$name.partial
trap 'rm -f "$tmp"' EXIT
mongodump --uri="$MONGO_URI" --archive --gzip --quiet | age "${recipients[@]}" > "$tmp"
mv "$tmp" "/backups/$name"
echo "backup: wrote /backups/$name ($(stat -c %s "/backups/$name") bytes)"

# Retention: delete all but the newest $keep backups.
ls -1 /backups/backd-*.archive.gz.age | sort -r | tail -n +$((keep + 1)) | while read -r old; do
  rm -f "$old"
  echo "backup: removed $old (keeping $keep)"
done
