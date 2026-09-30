#!/bin/bash
# Restores a backup written by backup.sh. Run with admin credentials in
# MONGO_URI and the age private key mounted at /run/backup-identity.
#
#   restore.sh <backup file> [mongorestore options...]
#
# Collections in the backup replace the existing ones (--drop); databases
# and collections not in the backup are left alone. Extra options go to
# mongorestore, for example --nsInclude='blog___system.*' to restore one
# database. Afterwards, run the provision job and restart backd.
set -euo pipefail
: "${MONGO_URI:?}"
file=${1:?usage: restore.sh <backup file> [mongorestore options...]}
shift
identity=${BACKUP_IDENTITY_FILE:-/run/backup-identity}
if [ ! -r "$identity" ]; then
  echo "restore: no age identity at $identity; mount your private key there" >&2
  exit 1
fi
age -d -i "$identity" "$file" | mongorestore --uri="$MONGO_URI" --archive --gzip --drop --quiet "$@"
echo "restore: restored $file; now run the provision job and restart backd"
