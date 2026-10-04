#!/bin/sh
# End-to-end test of the production reference deployment. Starts the stack
# under its own project name with fresh secrets in a temporary directory,
# checks each protection and an encrypted backup and restore, and removes
# everything afterwards (KEEP=1 keeps the stack running for inspection).
# Needs docker compose (or podman compose), curl and openssl.
#
#   deploy/production/test.sh
set -eu
cd "$(dirname "$0")"

export HTTPS_PORT=${HTTPS_PORT:-18443} HTTP_PORT=${HTTP_PORT:-18080}
# Only the probe container may reach the admin API.
export ADMIN_ALLOW_FROM=${PROBE_IP:-172.30.80.99}
project=backd-prod-test
work=$(mktemp -d)
./setup.sh "$work" >/dev/null
export BACKUP_DIR=$work/backups
mkdir -p "$BACKUP_DIR"
compose() { docker compose -p "$project" --env-file "$work/.env" -f compose.yaml -f test.compose.yaml "$@"; }
ops() { compose --profile ops "$@"; } # the backup and restore jobs
cleanup() {
  status=$?
  if [ "${KEEP:-}" = 1 ]; then
    echo "stack kept: docker compose -p $project --env-file $work/.env ..."
  else
    compose down -v >/dev/null 2>&1 || true
    rm -rf "$work"
  fi
  exit $status
}
trap cleanup EXIT INT TERM

base=https://localhost:$HTTPS_PORT
api=$base/v1/blog
ca=$work/secrets/ca.pem
fails=0
pass() { echo "ok   $1"; }
fail() { echo "FAIL $1"; fails=$((fails + 1)); }
check() { # check <description> <command...>: passes when the command succeeds
  desc=$1; shift
  if "$@"; then pass "$desc"; else fail "$desc"; fi
}
# logs <service>: the service's output (podman compose prints it on stderr).
logs() { compose logs "$1" 2>&1; }
# status <curl args...>: prints the HTTP status code.
status() { curl -s -o /dev/null -w '%{http_code}' --cacert "$ca" "$@"; }
# mongo_as <uri> <script>: runs mongosh in the ops image on the data network.
mongo_as() { compose run --rm -T --no-deps provision mongosh "$1" --quiet --eval "$2" 2>&1; }
tls="tls=true&tlsCAFile=/etc/backd/tls/ca.pem"
rs="replicaSet=rs0"
. "$work/.env"
root_uri="mongodb://root:$MONGO_ROOT_PASSWORD@mongo:27017/?$rs&authSource=admin&$tls"
app_uri="mongodb://backd:$BACKD_APP_PASSWORD@mongo:27017/?$rs&authSource=admin&$tls"

# wait_ready: until anonymous reads of published posts answer 200.
wait_ready() {
  i=0
  until [ "$(status "$api/main/posts")" = 200 ]; do
    i=$((i + 1))
    if [ $i -gt 90 ]; then echo "backd did not become ready"; compose logs backd | tail -20; return 1; fi
    sleep 2
  done
}

echo "building and starting the stack ($project)..."
compose up -d --build >/dev/null 2>&1
wait_ready

echo "--- TLS"
check "HTTP redirects to HTTPS" [ "$(curl -s -o /dev/null -w '%{http_code} %{redirect_url}' "http://localhost:$HTTP_PORT/v1/blog/main/posts")" = "301 https://localhost/v1/blog/main/posts" ]
check "HTTPS serves the API with a certificate the CA signed" [ "$(status "$api/main/posts")" = 200 ]
check "TLS 1.2 and 1.3 are accepted" sh -c "curl -sf -o /dev/null --cacert '$ca' --tlsv1.2 --tls-max 1.2 '$api/main/posts' && curl -sf -o /dev/null --cacert '$ca' --tlsv1.3 '$api/main/posts'"
check "TLS 1.1 is refused" sh -c "! curl -s -o /dev/null --cacert '$ca' --tlsv1.1 --tls-max 1.1 '$api/main/posts'"
check "HSTS header is sent" sh -c "curl -sI --cacert '$ca' '$api/main/posts' | grep -qi '^strict-transport-security: max-age=63072000'"
check "health endpoints stay internal" [ "$(status "$base/readyz")" = 404 ]

echo "--- request body size"
big=$work/big.json
head -c 1100000 /dev/zero | tr '\0' 'a' | sed 's/^/{"title":"/; s/$/"}/' > "$big"
check "a 1.1 MB body is refused by nginx with backd's error envelope" \
  sh -c "curl -s --cacert '$ca' -H 'Content-Type: application/json' --data-binary @'$big' -w ' %{http_code}' '$api/main/posts' | tr -d '\n' | grep -q '\"code\":\"payload_too_large\".* 413$'"

echo "--- client addresses"
# From the probe container, whose address (PROBE_IP) is known, with a
# spoofed X-Forwarded-For. nginx overwrites the header with the address it
# sees, and backd trusts it because it comes from PROXY_IP.
probe_ip=${PROBE_IP:-172.30.80.99}
rid=$(compose exec -T probe curl -s -D - -o /dev/null --cacert /ca.pem --connect-to localhost:443:nginx:443 \
  -H 'X-Forwarded-For: 203.0.113.9' https://localhost/v1/blog/main/posts 2>/dev/null | tr -d '\r' | sed -n 's/^x-request-id: //Ip')
sleep 1
seen=$(logs backd | grep "\"request_id\":\"$rid\"" | sed -n 's/.*"client":"\([^"]*\)".*/\1/p' | head -1)
echo "     request $rid from $probe_ip: backd logged client \"$seen\""
check "backd logs the client's address, not the spoofed X-Forwarded-For or nginx's" [ "$seen" = "$probe_ip" ]
# Published ports don't always keep the client address (rootless Podman
# replaces it); this shows what nginx sees for a request from this host.
rid=$(curl -s -D - -o /dev/null --cacert "$ca" "$api/main/posts" | tr -d '\r' | sed -n 's/^x-request-id: //Ip')
sleep 1
echo "     a request from this host reaches nginx from $(logs nginx | grep "rid=$rid" | sed 's/^.*| //' | awk '{print $1}' | head -1)"

echo "--- admin API restricted to ADMIN_ALLOW_FROM"
code=$(compose exec -T probe curl -s -o /dev/null -w '%{http_code}' --cacert /ca.pem --connect-to localhost:443:nginx:443 \
  https://localhost/v1/blog/_admin/users 2>/dev/null)
check "from the allowed address, requests reach backd (401 without a key)" [ "$code" = 401 ]
check "from elsewhere, the admin API answers 404 in backd's envelope" \
  sh -c "curl -s -w ' %{http_code}' --cacert '$ca' '$api/_admin/users' | tr -d '\n' | grep -q '\"code\":\"not_found\".* 404$'"
check "other routes are unaffected" [ "$(status "$api/main/posts")" = 200 ]
# The split: the public instance has no admin API at all, whoever asks;
# the internal one does (and needs a key).
code=$(compose exec -T probe curl -s -o /dev/null -w '%{http_code}' http://backd:8080/v1/blog/_admin/users 2>/dev/null)
check "the public instance answers 404 for the admin API, even directly" [ "$code" = 404 ]
code=$(compose exec -T probe curl -s -o /dev/null -w '%{http_code}' http://backd-admin:8080/v1/blog/_admin/users 2>/dev/null)
check "the internal instance serves it (401 without a key)" [ "$code" = 401 ]

echo "--- rate limits"
flood() { # flood <path> <body>: prints the status codes of 20 quick requests
  for i in $(seq 1 20); do
    status -X POST -H 'Content-Type: application/json' -d "$2" "$api/_auth/$1"
    echo
  done | sort | uniq -c | tr '\n' ' '
}
codes=$(flood signup '{"email":"flood@example.com","password":"x"}')
echo "     signup: $codes"
check "sign-up floods get 429" sh -c "echo '$codes' | grep -q ' 429'"
sleep 7 # let the auth bucket refill a little
codes=$(flood login '{"email":"nobody@example.com","password":"wrong password"}')
echo "     login:  $codes"
check "login floods get 429" sh -c "echo '$codes' | grep -q ' 429'"
sleep 7
codes=$(flood reset-password/request '{"email":"nobody@example.com"}')
echo "     reset:  $codes"
check "password-reset (emailing) floods get 429" sh -c "echo '$codes' | grep -q ' 429'"
check "429 uses backd's error envelope with Retry-After" \
  sh -c "curl -si --cacert '$ca' -X POST -H 'Content-Type: application/json' -d '{}' '$api/_auth/login' | tr -d '\r' | grep -q '^retry-after: 60' && \
         curl -s --cacert '$ca' -X POST -H 'Content-Type: application/json' -d '{}' '$api/_auth/login' | grep -q '\"code\":\"too_many_requests\"'"
check "reads are not limited by the auth zone" [ "$(status "$api/main/posts")" = 200 ]

echo "--- MongoDB"
check "connections without TLS are refused" sh -c "! mongo_out=\$(docker compose -p $project --env-file '$work/.env' run --rm -T --no-deps provision mongosh 'mongodb://mongo:27017/?serverSelectionTimeoutMS=3000' --quiet --eval 'db.adminCommand({ping:1})' 2>&1)"
out=$(mongo_as "mongodb://mongo:27017/?$tls" 'db.adminCommand({listDatabases: 1})' || true)
check "unauthenticated clients can't list databases" sh -c "echo '$out' | grep -q 'requires authentication'"
out=$(mongo_as "$app_uri" 'db.getSiblingDB("blog__main").posts.countDocuments({}); print("read-ok")' || true)
check "the app user reads documents" sh -c "echo '$out' | grep -q read-ok"
for op in 'db.getSiblingDB("blog__main").createCollection("x")' \
          'db.getSiblingDB("blog__main").unconfigured.insertOne({})' \
          'db.getSiblingDB("blog__main").posts.createIndex({title: 1})' \
          'db.getSiblingDB("blog__main").posts.drop()' \
          'db.getSiblingDB("other__db").x.insertOne({})' \
          'db.getSiblingDB("admin").system.users.findOne()' \
          'db.getSiblingDB("blog___system").audit.deleteMany({})' \
          'db.getSiblingDB("blog___system").audit.updateMany({}, {$set: {actor: "x"}})' \
          'db.getSiblingDB("blog___system").invocations.deleteMany({})' \
          'db.getSiblingDB("blog___system").invocations.updateMany({}, {$set: {status: "x"}})' \
          'db.getSiblingDB("blog___system").jobs.deleteMany({})'; do
  out=$(mongo_as "$app_uri" "$op" || true)
  check "the app user is refused: $op" sh -c "echo '$out' | grep -qi 'not authorized\|unauthorized'"
done

echo "--- first administrator and API keys"
# bootstrap writes the first administrator with backd's own least-privilege
# MongoDB user; from then on the CLI works through the API. The ops image
# has the CLI and reaches backd directly on the data network.
printf 'dev-p4ssw0rd!\n' | compose exec -T backd /backd bootstrap --realm blog --email ops@example.com >/dev/null 2>&1
out=$(printf 'x\n' | compose exec -T backd /backd bootstrap --realm blog --email eve@example.com 2>&1 || true)
bootstrap_refused() { printf '%s' "$out" | grep -q 'already has an administrator'; }
check "bootstrap refuses once the realm has an administrator" bootstrap_refused
# admin_cli runs backd commands in the ops image as ops@example.com: logs
# in, then runs the given script. $DRILL_PASSWORD is passed through.
admin_cli() {
  compose run --rm -T --no-deps -e BACKD_URL=http://backd-admin:8080 -e BACKD_CREDENTIALS=/tmp/credentials \
    -e DRILL_PASSWORD="${DRILL_PASSWORD:-}" provision sh -c \
    "printf 'dev-p4ssw0rd!\n' | backd login --realm blog --email ops@example.com >/dev/null && $1"
}
key=$(admin_cli 'backd apikey create --realm blog --name svc-test --expires 1d' 2>/dev/null | grep -o 'bdk_[A-Za-z0-9_-]*')
admin_cli 'backd audit --realm blog --limit 20' > "$work/audit.txt" 2>&1 || true
check "the audit trail records the bootstrap and the key" \
  sh -c "grep -q 'realm.bootstrap.*cli:bootstrap' '$work/audit.txt' && grep -q 'apikey.create.*key:svc-test' '$work/audit.txt'"
check "an API key created through the admin API writes through the edge" \
  [ "$(status -X POST -H "Authorization: Bearer $key" -H 'Content-Type: application/json' \
      -d '{"title":"Hello","body":"From the edge","published":true,"author":{"name":"Ops","email":"ops@example.com"}}' "$api/main/posts")" = 201 ]

echo "--- functions: executor, egress and worker (network placement, F4)"
# A second, function-only realm (functions/netprobe): its own administrator
# and API key, then one call proving the executor's network isolation from
# a real function in this stack's own topology (docs: Configuration ->
# Functions -> "Egress: the network allowlist, completed").
printf 'dev-p4ssw0rd!5\n' | compose exec -T backd /backd bootstrap --realm netprobe --email np@example.com >/dev/null 2>&1
netprobe_key=$(compose run --rm -T --no-deps -e BACKD_URL=http://backd-admin:8080 -e BACKD_CREDENTIALS=/tmp/np-credentials provision sh -c \
  "printf 'dev-p4ssw0rd!5\n' | backd login --realm netprobe --email np@example.com >/dev/null && backd apikey create --realm netprobe --name probe --expires 1d" \
  2>/dev/null | grep -o 'bdk_[A-Za-z0-9_-]*')
resp=$(curl -s --cacert "$ca" -X POST -H "Authorization: Bearer $netprobe_key" -H 'Content-Type: application/json' -d '{}' "$base/v1/netprobe/app/_func/network_probe")
check "layer 1: an undeclared host is refused" sh -c "echo '$resp' | grep -q '\"test\":\"unlisted-host\",\"reached\":false'"
check "layer 2: a declared metadata address is refused by egress (403)" sh -c "echo '$resp' | grep -q '\"test\":\"metadata\",\"reached\":true,\"status\":403'"
check "layer 2: a declared name resolving to MongoDB is refused by egress (403)" sh -c "echo '$resp' | grep -q '\"test\":\"mongo-fetch\",\"reached\":true,\"status\":403'"
check "layer 3: the same address has no route at all (raw TCP, bypasses egress)" sh -c "echo '$resp' | grep -q '\"test\":\"mongo-rawtcp\",\"reached\":false'"
# async_job_completes: enqueues network_probe_async and polls until the
# worker (claiming jobs through the same executor) marks it done.
async_job_completes() {
  job=$(curl -s --cacert "$ca" -X POST -H "Authorization: Bearer $netprobe_key" -H 'Content-Type: application/json' \
    -d '{}' "$base/v1/netprobe/app/_func/network_probe_async" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  [ -n "$job" ] || return 1
  for _ in $(seq 1 30); do
    st=$(curl -s --cacert "$ca" -H "Authorization: Bearer $netprobe_key" "$base/v1/netprobe/app/_jobs/$job" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')
    [ "$st" = done ] && return 0
    sleep 1
  done
  return 1
}
check "an async job still completes (the worker claims and runs it)" async_job_completes

echo "--- metrics (private port, Prometheus)"
# The edge has no route to the metrics port.
check "the public edge doesn't serve /metrics" sh -c "[ \"\$(curl -s -o /dev/null -w '%{http_code}' --cacert '$ca' '$base/metrics')\" = 404 ]"
# The port asks for the token: from the func network (where function
# processes live) without it, and nothing else listens there.
out=$(compose exec -T executor wget -S -qO- http://backd:9090/metrics 2>&1 || true)
check "the metrics port refuses a request without the token" sh -c "echo '$out' | grep -q '401'"
out=$(compose exec -T executor wget -qO- http://prometheus:9090/ 2>&1 || true)
case "$out" in
  *'<html'*|*'<title>'*|*'"status"'*) fail "Prometheus' own port isn't reachable from the func network" ;;
  *) pass "Prometheus' own port isn't reachable from the func network" ;;
esac
# Prometheus: its config and rules are valid, and it scrapes every process.
out=$(compose exec -T prometheus promtool check config /etc/prometheus/prometheus.yml 2>&1 || true)
check "Prometheus' config and the alert rules are valid" sh -c "echo '$out' | grep -q 'SUCCESS'"
prom() { compose exec -T prometheus wget -qO- "http://127.0.0.1:9090$1" 2>/dev/null; }
up=0
for _ in $(seq 1 40); do
  up=$(prom '/api/v1/query?query=count(up%7Bjob%3D%22backd%22%7D%3D%3D1)' | sed -n 's/.*"value":\[[0-9.]*,"\([0-9]*\)".*/\1/p')
  [ "${up:-0}" = 4 ] && break
  sleep 3
done
check "Prometheus scrapes the API, worker, executor and egress (up: ${up:-0} of 4)" [ "${up:-0}" = 4 ]
for series in backd_build_info backd_http_requests_total backd_mongodb_operation_duration_seconds_count backd_mongodb_up backd_jobs backd_function_invocations_total backd_egress_requests_total backd_executor_runs_total; do
  n=0
  for _ in $(seq 1 20); do
    n=$(prom "/api/v1/query?query=count($series)" | sed -n 's/.*"value":\[[0-9.]*,"\([0-9]*\)".*/\1/p')
    [ "${n:-0}" -gt 0 ] 2>/dev/null && break
    sleep 3
  done
  check "the series $series exists" [ "${n:-0}" -gt 0 ]
done
out=$(prom '/api/v1/query?query=backd_http_requests_total')
check "no email, token or identifier is in a label" sh -c "! echo '$out' | grep -Eq '@|bd[a-z]_|[0-9a-v]{20}'"

echo "--- PROVISION_MODE=verify"
mongo_as "$root_uri" 'db.getSiblingDB("blog__main").posts.dropIndexes()' >/dev/null
# A second backd with the same settings, as after a restart.
out=$(compose run --rm -T --no-deps backd 2>&1 || true)
config_mismatch_refused() { printf '%s' "$out" | grep -q 'MongoDB does not match the config (run .backd provision.)'; }
check "backd refuses to start when MongoDB differs from the config" config_mismatch_refused
compose stop backd >/dev/null 2>&1
check "while backd is down, nginx answers 503 with backd's error envelope" \
  sh -c "curl -s -w ' %{http_code}' --cacert '$ca' '$api/main/posts' | tr -d '\n' | grep -q '\"code\":\"unavailable\".* 503$'"
compose run --rm -T provision >/dev/null 2>&1
compose up -d backd >/dev/null 2>&1
check "after the provision job, backd starts again" wait_ready

echo "--- config as a deployable artifact"
fp=$(compose exec -T backd /backd config fingerprint 2>/dev/null | tr -d '\r')
compose exec -T probe curl -s http://backd:8080/readyz > "$work/readyz.json" 2>/dev/null || true
check "backd reports the fingerprint of the config it runs in /readyz" \
  sh -c "[ -n '$fp' ] && grep -q '\"config\":\"$fp\"' '$work/readyz.json'"
# An instance left on another config (say, an older tag) doesn't start.
mkdir -p "$work/other-config" && cp -r ../../examples/config/blog "$work/other-config/"
printf '\n# another version of the config\n' >> "$work/other-config/blog/realm.yaml"
# The copied source has no built function bundle (.build/ is gitignored,
# built into the image at Dockerfile build time instead): reuse the one
# already baked into the running backd image (docker cp — backd's own
# image is distroless, with no shell to build or extract with any other
# way) rather than rebuilding it here, which would need Deno to reach the
# internet for its own dependencies (esbuild) on a cold cache — exactly
# what the executor's network is deliberately isolated from.
# The container's name depends on the tool (docker compose v2 names it
# <project>-backd-1, podman-compose <project>_backd_1): ask compose for it.
backd_ctr=$(compose ps -aq backd 2>/dev/null | head -n1)
docker cp "${backd_ctr:-${project}_backd_1}:/config/blog/main/_functions/.build" "$work/other-config/blog/main/_functions/.build" 2>"$work/other-build.log" ||
  { echo "copying the built function bundle for the other-config drill failed:"; cat "$work/other-build.log"; }
compose run --rm -T --no-deps -v "$work/other-config/blog:/config/blog:ro" backd > "$work/other.log" 2>&1 || true
check "backd refuses to start with a config other than the provisioned one" \
  grep -q 'the config differs from the one last provisioned' "$work/other.log"
out=$(mongo_as "$app_uri" 'db.getSiblingDB("backd___deployment").realms.updateOne({}, {$set: {fingerprint: "x"}})' || true)
check "the app user can't change the provisioned fingerprint" sh -c "echo '$out' | grep -qi 'not authorized\|unauthorized'"

echo "--- encrypted backup and restore"
# The backup key pair; in production it's created away from the server.
(umask 077; compose run --rm -T --no-deps provision age-keygen 2>/dev/null > "$work/identity.txt")
recipient=$(sed -n 's/^# public key: //p' "$work/identity.txt")
out=$(BACKUP_RECIPIENTS= ops run --rm -T --no-deps backup 2>&1 || true)
check "backups refuse to run without a recipient" sh -c "echo '$out' | grep -q 'BACKUP_RECIPIENTS is empty'"
# Data to round-trip: a user with a password and a draft only they can
# read, an API key (svc-test) and a published post.
DRILL_PASSWORD='dev-p4ssw0rd!6' admin_cli 'printf "%s\n" "$DRILL_PASSWORD" | backd user create --realm blog --email drill@example.com' >/dev/null 2>&1
# login: a session token for the drill user, retrying while the sign-in
# rate limit (tested above) still refuses.
login() {
  for _ in $(seq 1 30); do
    out=$(curl -s --cacert "$ca" -X POST -H 'Content-Type: application/json' \
      -d '{"email":"drill@example.com","password":"dev-p4ssw0rd!6"}' "$api/_auth/login")
    tok=$(echo "$out" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
    [ -n "$tok" ] && { echo "$tok"; return 0; }
    sleep 3
  done
  return 1
}
tok=$(login)
draft=$(curl -s --cacert "$ca" -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
  -d '{"title":"Drill draft","published":false,"author":{"email":"drill@example.com"}}' "$api/main/posts" \
  | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
check "the drill user saves a private draft" [ -n "$draft" ]
BACKUP_RECIPIENTS="$recipient" ops run --rm -T --no-deps backup >/dev/null 2>&1
backup=$(ls "$BACKUP_DIR"/backd-*.archive.gz.age 2>/dev/null | head -1)
check "a backup file is written" [ -s "$backup" ]
check "the backup is age-encrypted, without plaintext" \
  sh -c "head -c 21 '$backup' | grep -q '^age-encryption.org/v1' && ! gunzip -c '$backup' >/dev/null 2>&1 && ! grep -q drill@example.com '$backup'"

# Lose the server: remove the whole stack and MongoDB's volume, then
# restore into a new, empty MongoDB, following the runbook: MongoDB alone,
# restore, the provision job, then backd. MongoDB's entrypoint creates only
# the admin user again, from .env.
compose down >/dev/null 2>&1
docker volume rm "${project}_mongo-data" >/dev/null 2>&1 || true
check "the old MongoDB volume is gone" sh -c "! docker volume inspect '${project}_mongo-data' >/dev/null 2>&1"
compose up -d --no-deps mongo >/dev/null 2>&1
# Its health check initiates the new replica set; wait until it's the primary.
for _ in $(seq 1 60); do
  mongo_as "$root_uri" 'print("primary=" + db.hello().isWritablePrimary)' | grep -q 'primary=true' && break
  sleep 2
done
out=$(mongo_as "$root_uri" 'db.adminCommand({listDatabases: 1, nameOnly: true}).databases.map(d => d.name).join(",")')
check "the new MongoDB has no backd databases" sh -c "! echo '$out' | grep -q 'blog__'"
compose run --rm -T --no-deps provision age-keygen 2>/dev/null > "$work/other.txt"
out=$(ops run --rm -T --no-deps -v "$work/other.txt:/run/backup-identity:ro" restore "/backups/$(basename "$backup")" 2>&1 || true)
check "another key can't decrypt the backup" sh -c "echo '$out' | grep -qi 'no identity matched'"
ops run --rm -T --no-deps -v "$work/identity.txt:/run/backup-identity:ro" restore "/backups/$(basename "$backup")" >/dev/null 2>&1
# The provision job reapplies validators and indexes and recreates the
# least-privilege users, which the dump doesn't carry; then the stack.
compose run --rm -T --no-deps provision >/dev/null 2>&1
compose up -d >/dev/null 2>&1
check "backd starts on the restored databases (verify mode)" wait_ready
tok=$(login || true)
check "the restored user logs in with their password" [ -n "$tok" ]
check "they read their own draft" sh -c "curl -s --cacert '$ca' -H 'Authorization: Bearer $tok' '$api/main/posts/$draft' | grep -q 'Drill draft'"
check "and it's still private" [ "$(status "$api/main/posts/$draft")" = 404 ]
check "the restored API key works" \
  [ "$(status -H "Authorization: Bearer $key" "$api/main/posts")" = 200 ]
check "the restored published post is there" sh -c "curl -s --cacert '$ca' '$api/main/posts' | grep -q 'From the edge'"

echo
if [ $fails -gt 0 ]; then echo "$fails check(s) failed"; exit 1; fi
echo "all checks passed"
