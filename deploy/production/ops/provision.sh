#!/bin/sh
# Deploy step, run with admin credentials in MONGO_URI:
#   1. `backd provision`: collections, validators, indexes, system databases;
#   2. grants.js: the service user BACKD_APP_USER gets document access to
#      exactly the collections `backd databases --collections` lists, and
#      listCollections on every database `backd databases` lists (the two
#      differ for a functions-only database, which has no collections of
#      its own but PROVISION_MODE=verify still lists it).
# Idempotent. Run it again after every config change, before starting backd.
set -eu
: "${MONGO_URI:?}" "${BACKD_APP_USER:?}" "${BACKD_APP_PASSWORD:?}"

backd provision
COLLECTIONS=$(backd databases --collections)
DATABASES=$(backd databases)
export COLLECTIONS DATABASES
mongosh "$MONGO_URI" --quiet --file /ops/grants.js
