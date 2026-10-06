#!/bin/sh
# One-shot, run after MinIO starts: the bucket the realms' files use, a policy that
# lets backd's key put, get and delete under the `prod/` prefix of that bucket and
# list it, and the key itself. The root password is only for this job. Idempotent.
set -eu
: "${MINIO_ROOT_PASSWORD:?}" "${STORAGE_ACCESS_KEY:?}" "${STORAGE_SECRET_KEY:?}"
# mc trusts this deployment's CA through SSL_CERT_FILE (set in compose.yaml).
export MC_HOST_local="https://minio-root:${MINIO_ROOT_PASSWORD}@minio:9000"

i=0
until mc ls local >/dev/null 2>&1; do
  i=$((i + 1))
  [ "$i" -gt 60 ] && { echo "MinIO did not become ready" >&2; exit 1; }
  sleep 2
done
mc mb --ignore-existing local/backd-files
mc admin policy create local files-prod /ops/storage-policy.json
mc admin user add local "$STORAGE_ACCESS_KEY" "$STORAGE_SECRET_KEY"
mc admin policy attach local files-prod --user "$STORAGE_ACCESS_KEY" || true
echo "storage ready: bucket backd-files, key $STORAGE_ACCESS_KEY limited to prod/"
