---
title: "Operations"
description: "MongoDB, provisioning, health checks, logging, timeouts, shutdown, and running backd in production."
icon: "monitor_heart"
weight: 400
toc: true
---

## MongoDB

`backd` needs MongoDB 8.2 running as a **replica set**. A single node is fine. Replica sets support transactions, which [atomic batch writes](../api/documents/#batch-writes) rely on, and every setup `backd` is tested with uses one. Sharded clusters (through `mongos`) work too.

At startup, `backd` asks the server what it is. It refuses to start against a standalone server:

```
mongodb: the server is a standalone instance; backd needs a replica set (a single node is fine): start mongod with --replSet and run rs.initiate(), then add ?replicaSet=<name> to MONGO_URI
```

Managed services such as MongoDB Atlas always run replica sets. To run a single node yourself:

```sh
docker run -d --name mongo -p 127.0.0.1:27017:27017 docker.io/library/mongo:8.2 --replSet rs0
docker exec mongo mongosh --quiet --eval \
  "rs.initiate({_id: 'rs0', members: [{_id: 0, host: 'localhost:27017'}]})"
export MONGO_URI='mongodb://localhost:27017/?replicaSet=rs0'
```

- The member's `host` is the address clients use to reach it: `localhost:27017` for a server on your machine, the service name (such as `mongo:27017`) inside a compose network.
- `replicaSet=rs0` in `MONGO_URI` names the set. Without it, the driver still discovers the set from the host you give.
- With authentication, a replica set also needs a key file (`--keyFile`), even with one node. The [production reference](production/#mongodb-with-authentication-and-tls) shows how.
- `make example` and the test setups start a single-node replica set and initiate it from the container's health check.

### Upgrading an existing standalone server

Restart it as a replica set and initiate it once; the data stays in place:

1. Stop `backd`.
2. Restart `mongod` with `--replSet rs0` (and `--keyFile` if authentication is on).
3. Run `rs.initiate({_id: 'rs0', members: [{_id: 0, host: '<host>:27017'}]})` once, as the admin user if authentication is on.
4. Add `replicaSet=rs0` to `MONGO_URI` and start `backd`.

## Provisioning

At startup, `backd` makes sure MongoDB matches the config. What it does depends on `PROVISION_MODE`.

**`apply` (default)**

- Creates each missing collection with its `$jsonSchema` validator. MongoDB creates the database implicitly.
- Updates (`collMod`) validators that differ from `schema.json`.
- Creates indexes declared in `indexes.json` that don't exist yet.
- Creates or updates the [system database](#realm-system-databases) of every realm with `auth: enabled`.
- Records the config's fingerprint for each realm (see [Deploying config](deploying/#what-backd-enforces)).
- Is idempotent and safe to run on every start.
- If a declared index exists with a different `unique` setting, `apply` stops with an error rather than dropping it. Drop the index manually and restart.
- Creating a unique index fails if the collection already holds duplicate values. The error names the index.

{{< hint style="tip" title="Already done for you" >}}
This is all done for you at startup: validators, declared indexes, the system databases and their TTL indexes. Nothing is ever **dropped**: a collection, index or database that exists in MongoDB but not in your config is kept and reported as a warning, so a config mistake can't delete data.
{{< /hint >}}

**`verify`**

- Only compares collections, validators and indexes with the config, including the system databases.
- If anything differs, it refuses to start and lists every difference. For example:

  ```
  backd serve: MongoDB does not match the config (run `backd provision`):
    shop__orders.items: collection missing
    shop__orders.clients: validator differs from schema.json
    shop__orders.clients: index (email) missing
  ```

- Then compares the config's fingerprint with the one provisioning recorded for each realm, and refuses to start if it differs or none was recorded: every instance must run the config that was provisioned (see [Deploying config](deploying/#what-backd-enforces)).
- Needs only read/write privileges on the data, and read access to `backd___deployment.realms`.

**Common to both modes**

- Validators always use `validationLevel: "moderate"` and `validationAction: "error"`.
- **Nothing is ever dropped.** Some databases, collections and indexes exist in MongoDB but not in the config: databases named like `<realm>__<database>`, collections inside configured databases, or indexes not in `indexes.json`. `backd` doesn't serve those databases and collections, keeps those indexes, and logs a warning for each. The same goes for a `<realm>___system` database whose realm is gone or has `auth: disabled`: it is kept and reported, never used.
- Schema keywords that MongoDB can't enforce are logged at `info` level (see [Schema validation](../configuration/validation/)).

`backd provision` runs `apply` once and exits.

Both `backd provision` and `backd serve` then apply the [role assignments](../configuration/realm/#roles) seeded in each realm's `realm.yaml`.

### Realm system databases

Each realm with `auth: enabled` has a system database named `<realm>___system` (three underscores). It holds the realm's users and credentials, managed through the [admin API](../auth/admin/) (for example with [`backd user`](../auth/users/)); only [`backd bootstrap`](../auth/cli/#the-first-administrator) writes to it directly. Realm names must be short enough for this name to stay under MongoDB's 64-character limit, so a realm name can be at most 54 characters.

| Collection | Holds | Indexes |
|---|---|---|
| `users` | email, verified flag, roles, disabled flag | `email` (unique, sparse) |
| `identities` | sign-in methods per user: a password hash today, external providers later | `provider` + `subject` (unique), `user_id` |
| `sessions` | sessions: public id, hashed token, last use and expiry | `token_hash` (unique), `user_id`, `expires_at` (TTL) |
| `api_keys` | hashed [API keys](../auth/api-keys/) with name, first characters, last use and expiry | `name` (unique) |
| `invitations` | [invitations](../auth/admin/#invitations): public id, hashed token, optional email | `token_hash` (unique), `expires_at` (TTL) |
| `login_attempts` | failed-login counters per email and client address; reused, under their own key namespace, for [function rate limits](../functions/calling/#rate-limits) | `expires_at` (TTL) |
| `audit` | the [audit trail](../auth/audit/): append-only records of security-sensitive actions | `at`, `target` + `at`, `expires_at` (TTL) |
| `secrets` | [function secrets](../functions/secrets/), encrypted at rest (never their plaintext values) | `database` + `name` (unique) |
| `invocations` | [function invocation history](../functions/logs/): append-only, what happened — never a call's input or output | `function` + `at`, `request_id` (sparse), `expires_at` (TTL) |
| `jobs` | [async function jobs](../functions/jobs/): input, status, and once done, result — the one exception to never storing input/output, since there's nowhere else to keep it until read | `status` + `lease_expires` + `created_at`, `expires_at` (TTL) |
| `idempotency` | [`Idempotency-Key` claims](../functions/calling/#idempotency): scoped to the function and caller, a sync call's stored response or an async call's job id | `expires_at` (TTL) |

Each collection has a `$jsonSchema` validator that checks its required fields and their types. The TTL indexes make MongoDB delete expired sessions, invitations, counters, audit records, invocation records, jobs and idempotency claims automatically. If one of these indexes exists with different options (for example `expires_at` without TTL, so sessions would never expire), `apply` and `verify` fail and name it; drop the index and let `backd` recreate it.

Treat the system databases as sensitive: they contain password hashes and token hashes. Back them up with the rest of the realm's data, encrypted: only role seeds can be rebuilt from the config. Tokens and keys are stored only as hashes, so a leaked backup doesn't reveal usable credentials, but password hashes can still be attacked offline. See [Backup and restore](backup/).

### Least-privilege deployments

The service's MongoDB user doesn't need to create collections, change validators or build indexes. Run provisioning as a separate deploy step instead:

1. Run `backd provision` with an admin-privileged `MONGO_URI`.
2. Grant the service's user `find`, `insert`, `update`, `remove` and `listIndexes` on each collection that `backd databases --collections` prints, and `listCollections` on their databases. MongoDB privileges can't match names by pattern, so repeat this step whenever the config changes.
3. Start the service with `PROVISION_MODE=verify` and that user.

{{< hint style="tip" title="Best practice" >}}
In production, run `backd provision` once per release with an admin URI and start every instance with `PROVISION_MODE=verify` and a user that can only read and write the configured collections. A compromised instance then can't change validators, indexes or collections.
{{< /hint >}}

Grant collections rather than MongoDB's built-in `readWrite` role on whole databases: `readWrite` also allows creating, dropping and re-indexing collections, and MongoDB lets `insert` on a database create any collection in it. The [production reference](production/#least-privilege-provisioning) automates all three steps.

`backd databases` needs only `CONFIG_DIR`. It prints every data database (`<realm>__<database>`) and the system database (`<realm>___system`) of every realm with `auth: enabled`. With `--collections`, it prints every collection as `<database>.<collection>`, including the system collections:

```sh
$ CONFIG_DIR=./config backd databases
blog___system
blog__main
shop__orders
$ CONFIG_DIR=./config backd databases --collections
blog___system.api_keys
…
blog__main.posts
shop__orders.items
```

### Upgrading to v0.4.0

v0.4.0 adds [email flows](../functions/email/), [erasure](../auth/erasure/), [metrics](metrics/) and the npm client. Configuration that worked on v0.3.0 still loads and nothing new is required, but two behaviors change, so read the first two items.

1. **Deleting a user means something else.** `DELETE /_auth/me` (a user deleting their own account) now **deactivates** it: the account is disabled and its sessions end, but the data and the email address are kept, and an administrator can reactivate it. `DELETE /_admin/users/{id}` and `backd user delete` now **erase**: the user becomes a tombstone (`erased-<id>@erased.invalid`, so the real address is free again) and a worker applies the `collection.yaml` policy of each collection that declares one; they answer `202` with an erase job, and `backd user delete` waits for it. Before, both removed the user and left every document alone. To get that effect now, declare no policy: an erase without policies leaves the documents as they are. A worker must be running (`backd worker`, or `serve --with-worker`); see [Deleting and erasing users](../auth/erasure/).
2. **Provision before rolling out**, as always: each realm's system database gains collections and indexes (email tokens, an index of erase jobs), and `PROVISION_MODE=verify` refuses to start without them. The worker now claims jobs in every realm with authentication, not only those with functions.
3. **Some addresses are refused.** Addresses on the reserved `.invalid` domain, with a comma, semicolon, colon, angle bracket, parenthesis, square bracket, quote or backslash, or longer than 254 characters no longer pass, at sign-up and when looked up. An existing account with such an address can't sign in by email until an administrator changes the address in the database. Ordinary addresses are not affected.
4. **If you copied the [production reference](production/)**: the stricter rate-limit zone now covers password reset, verification, invitations and address changes (`nginx/backd.conf.template`), and it gained a Prometheus. Run its `setup.sh` again: it adds `BACKD_METRICS_TOKEN` to your `.env` and `secrets/metrics-token`.
5. **The JavaScript client is on npm as `backd-js`** (`npm install backd-js`). It was never published as `@backd/client`: if you installed it from a checkout, change the import. Versions of `backd-js` before 0.2 were an unrelated, older library.
6. **Metrics, email and the `account` settings are all off by default.** Turn them on when you want them: `METRICS_ADDR`, the `email` section of `realm.yaml` and `account.*`.

### Upgrading to v0.2.0

v0.2.0 adds users and access rules and is a breaking release: every realm needs a [`realm.yaml`](../configuration/realm/), and authentication is on by default. To keep an existing realm working as before on a private network, give it a `realm.yaml` with `auth: disabled`; to protect it, provision it and give server-side clients [API keys](../auth/api-keys/).

1. **Add a `realm.yaml` to every realm.** To keep a realm working exactly as before, without authentication, put `auth: disabled` in it. Only do this for realms that stay on a private network: anyone who can reach them can read and modify their data.
2. **Or turn authentication on** (the default, `auth: enabled`), declare an admin role in `realm.yaml` (`roles: {admin: {admin: true}}`), then:
   - run `backd provision` (or start `backd serve` with `PROVISION_MODE=apply`) to create the realm's system database;
   - create the realm's first administrator with `backd bootstrap --realm <realm> --email <email>`, and log in as them with `backd login --realm <realm> --url <server>`;
   - give server-side clients an API key: `backd apikey create --realm <realm> --name <name> --expires 90d`, sent as `Authorization: Bearer <key>`;
   - add a `rules.yaml` to collections that users or anonymous callers should reach. Without one, only API keys can.
3. **Existing documents** don't have ownership fields. They keep working: `_meta.owner` reads as `null`, so owner-based rules don't match them, and their next write adds `_meta.updated_by`. API keys can still manage them.
4. **Behind a reverse proxy**, set `TRUSTED_PROXIES` so login throttling sees real client addresses.
5. **Browser apps** on another origin need that origin under `cors.origins` in `realm.yaml`.

## Health checks

Use `GET /healthz` as the liveness probe and `GET /readyz` as the readiness probe (see [HTTP API](../api/#health-endpoints)).

## Logging

Logs are JSON lines on standard error, filtered by `LOG_LEVEL`.

- At startup, `backd` logs a `WARN` line for each realm with `auth: disabled` (its data is open to anyone who can reach the service), and for each realm with `auth: enabled` but no `rules.yaml` in any collection (only API keys can reach its data). It warns about realms with `auth: enabled` that nobody can administer with a session (no admin role, or no user holding one). It also lists API keys that never expire or expire within 14 days (see the [expiry policy](../auth/api-keys/#expiry-and-rotation-policy)).
- Each request produces one access-log line (`"msg":"request"`). It includes the method, route pattern, realm, database, collection, status, duration, bytes, `request_id` and the client's address (`client`, following [`TRUSTED_PROXIES`](#client-addresses-behind-a-proxy)), plus `actor` in realms with `auth: enabled`: `user:<id>` for a session, `key:<name>` for an API key, `key:<name> as user:<id>` for an API key acting on behalf of a user, or `anonymous`.
- At `debug` level, requests refused by [access rules](../auth/rules/#answers-when-a-rule-says-no) are logged (`"msg":"access denied"`) with the collection, operation and reason.
- Request bodies, document contents, passwords and tokens are never logged.

## Timeouts

| Timeout | Value |
|---|---|
| Storage work per document request | `MONGO_OP_TIMEOUT` (default 10s). Exceeding it returns `503 unavailable` |
| Reading request headers | 10s |
| Reading the whole request | 30s |
| Writing the response | `MONGO_OP_TIMEOUT` + 10s, at least 30s |
| Idle keep-alive connections | 120s |

## Shutdown

On `SIGINT` or `SIGTERM`, `backd`:

1. closes its listener at once, so new connections and readiness probes are refused;
2. lets in-flight requests finish, for up to `SHUTDOWN_TIMEOUT` (default 15s);
3. closes the MongoDB client and exits with status 0.

Requests still running after the timeout are aborted, and `backd` exits with a non-zero status.

Give your process manager a stop grace period longer than `SHUTDOWN_TIMEOUT`:

- the included `docker-compose.yml` uses `stop_grace_period: 20s`;
- Docker's default is 10s;
- Kubernetes' default `terminationGracePeriodSeconds` is 30s.

`backd` serves plain HTTP. Terminate TLS in front of it with a reverse proxy, ingress or load balancer. The [production reference deployment](production/) does this and everything else below. Before going public, go through the [hardening checklist](checklist/). Before exposing a realm, also go through the [security checklist](../auth/security/#checklist-before-exposing-a-realm).

### Rate limiting and abuse

`backd` doesn't limit request rates, except for [failed logins](../auth/sessions/#brute-force-protection). Put a reverse proxy, API gateway or CDN in front of it and limit there (the [production reference](production/#rate-limits) has a tested nginx configuration):

- **Sign-up:** in realms with `signup: open`, limit `POST /v1/{realm}/_auth/signup` per client address, or use `signup: invite`.
- **Public data:** collections whose `read` rule lets anonymous callers in can be scraped; limit or cache them.
- **Everything else:** a general per-address limit protects MongoDB from floods.

Password hashing is bounded by `PASSWORD_HASH_CONCURRENCY` whatever the traffic: excess logins queue, then answer `503` at their deadline, instead of exhausting memory.

### Client addresses behind a proxy

[Login throttling](../auth/sessions/#brute-force-protection) counts failures per client address. Behind a proxy, every connection comes from the proxy, so set `TRUSTED_PROXIES` to the proxies' exact addresses:

```sh
TRUSTED_PROXIES=172.30.80.10
```

List networks only if every address in them is one of your proxies. Whole private ranges such as `10.0.0.0/8` would let any other container or host in them forge `X-Forwarded-For` (see [client addresses](production/#client-addresses) in the production reference).

- `X-Forwarded-For` is only believed when the connection comes from a trusted proxy. Otherwise clients could pick any address they like.
- The client is the right-most address in `X-Forwarded-For` that isn't a trusted proxy. Entries further left were supplied by the client and are ignored.
- The proxy in front of `backd` must overwrite `X-Forwarded-For` with the address it sees, unless it trusts a load balancer in front of it. The [production reference](production/#client-addresses) shows how. The local stack (`make example`) trusts all private ranges for convenience: don't copy that.
- Without `TRUSTED_PROXIES`, `backd` uses the connection's address. Behind a proxy, all clients then share one counter, and a flood of failures from anyone slows down logins for everyone behind that proxy.
