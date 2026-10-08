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

### Upgrading to v0.9.0

1. **`backd` without a command prints the help; it no longer serves.** Before, `backd` alone ran `serve`. Anything that starts the binary or the container image with no command must say `serve` now: a `command: ["serve"]` in a compose file, `args: ["serve"]` in Kubernetes, `docker run … backd serve`, `ExecStart=/usr/local/bin/backd serve`. The images now have `CMD ["serve"]`, so `docker run backd` and a container that overrides nothing still serve, but a service that sets its own `entrypoint` or runs the binary directly does not. The shipped compose files say it explicitly.
2. **`backd files regenerate`** is new (see [File fields](../files/file-fields/#after-you-change-a-declaration)); `backd` and `backd help` show the commands grouped by who uses them.

### Upgrading to v0.8.0

v0.8.0 adds [files](../files/), [schema checks](../configuration/validation/#finding-documents-that-no-longer-match), [`on_complete`](../functions/jobs/) and [job steps](../functions/jobs/), and more control over [schedules](../functions/cron/) (pausing, `timezone`, `overlap: skip`). Configuration that worked on v0.7.0 still loads, except for the one case in point 2; each new feature is off until you use it. What to check:

1. **Provision before rolling out**, as always. New system collections are created in each realm's `___system` database: `file_journal`, `file_deletions`, `storage_usage`, `schema_checks` and `schedules`. `PROVISION_MODE=verify` refuses to start until `backd provision` has made them.
2. **A file field can't be declared in `schema.json`.** Files are declared under `files:` in `collection.yaml` (backd adds their schema), and startup refuses a collection that has the same property in both, and one with `files:` in a realm without `storage:`. If you used a property to hold file details, move it. The tutorial's `assets.file` placeholder moved this way.
3. **Files are opt-in, and need more than configuration.** A realm declares [`storage:`](../files/storage/) in `realm.yaml` and sets its two keys as realm secrets (`backd secret set`, with `BACKD_SECRETS_KEY` on the instance); without them the realm's file endpoints answer `503 storage_unavailable` and reads of its documents come without links. **Run workers** (`backd worker`, or `--with-worker`): they delete the objects of replaced, removed and abandoned files and finish uploads a crash interrupted. Set **`BACKD_URL`** in production, since backd-signed links name it. `BACKD_MAX_UPLOAD_BYTES` (default 100 MiB) caps proxy uploads.
4. **Give each backd instance its own storage `prefix`**, and read [Security of files](../files/security/): links and caches outlive later rule changes, and a function can't declare the storage secrets or `BACKD_FILES_LINK_KEY` as its own (startup refuses it).
5. **Erasing a user** now also queues the files of the documents the policy deletes or anonymizes (a `remove` of a file field). A `DELETE`, a batch delete and a purge queue the document's files too. MongoDB's own retention of a soft-deleted document can't: [`backd storage reconcile`](../files/maintenance/#reconciling) finds those objects.
6. **The admin UI** gains the file controls and schema checks. Its `Content-Security-Policy` now also allows images and direct uploads from the origin of the realms' file storage (and from no other origin besides its own); a stack without storage has the policy it had.
7. **API and CLI additions:** the `_files` routes and `?file_links=true` on reads; `GET /_admin/storage`, `POST /_admin/storage/check` and `/reconcile`; `/_admin/data-checks`; `/_admin/schedules`; `backd storage check|usage|reconcile`, `backd data check|checks`, `backd functions schedules|pause|resume`. New error codes: `too_many_files`, `upload_mode_mismatch`, `unsupported_file_type`, `file_missing`, `invalid_file_link`, `upload_mismatch`, `file_not_uploaded` and `storage_unavailable`. The `owned` report lists `replace` as `[]`, no longer `null`, for a policy that replaces nothing.
8. **The tutorial's stack** now runs a MinIO: fetch `compose.yaml` and `nginx.conf` again before chapter 13.
9. **The JavaScript client is 0.7.0** (`npm install backd-js@latest`): `files(id, field)`, `uploadFile`, `prepareUpload`, `fileUrl`, `downloadFile`, `deleteFile`, `fileLinks`, `admin.storage.status()` and `reconcile()`, `admin.dataChecks`, `admin.schedules`, `job.steps` and `wait({ onProgress })`. It works with a v0.7.0 server for everything that existed before.

### Upgrading to v0.7.0

v0.7.0 adds the [admin web interface](../auth/admin-ui/), a [read-only admin level](../auth/admin/#the-read-only-level) with `admin.read_access`, an audited [admin data route](../auth/admin/#data), a read-only [configuration view](../auth/admin/#configuration) in the admin API, and a switch that turns the admin API off per instance. Configuration that worked on v0.6.0 still loads; the interface is off until you switch it on. What to check:

1. **`admin: true` and admin API keys now reach documents.** The new [admin data route](../auth/admin/#data) (`/v1/{realm}/_admin/data/…`) lists, reads and changes documents **past the collection's access rules**, and it belongs to every `admin: true` role and every admin API key. Writes are audited without their content; reads are not (they carry the actor in the access log). Before upgrading, look at who holds those roles and keys: give people who don't need documents an area list (`admin: [users, invitations]`), which doesn't include the new `data` area, and keep admin keys to the tooling that needs them.
2. **Two new areas.** An area list can now also name `data` and `config`; a role that lists areas and doesn't name them doesn't get them. `config` shows what the instance runs for the realm (`GET /_admin/config`) and every read-only level has it.
3. **The read-only level is new.** `admin: read` (or `[read, secrets]`) reads every area except users and data, which a realm opts in to with `admin.read_access.users` and `admin.read_access.data` (both `false` by default). `GET /_admin/whoami` tells a client what the signed-in level may do.
4. **The admin API can be switched off per instance:** `BACKD_ADMIN_API=false` leaves every `/_admin` route out (`404`). The [production reference](production/#admin-api) now splits a public `backd` (no admin API) from an internal `backd-admin`, and the CLI's `BACKD_URL` points at the internal one: if you built on `deploy/production`, compare your compose file with it.
5. **The admin web interface is off by default.** `BACKD_ADMIN_UI=true` serves it at `/_ui/` from an instance that also has the admin API (the combination with `BACKD_ADMIN_API=false` stops startup); `BACKD_ADMIN_UI_IDLE` (default `30m`) signs an idle administrator out. Release images and binaries embed it; a binary built from source needs `make ui` (Node) first, and without it `/_ui/` answers `404` and startup warns. Serve it only from the internal instance, and read its [security review](../auth/admin-ui/#security-review) and the [hardening checklist](checklist/) first.
6. **Smaller changes:** `GET /_admin/users` takes `q` (a search of at most 254 characters), `GET /_admin/users/{id}/sessions` and `DELETE …/sessions/{session_id}` list and end a user's sessions. The example stack serves `/_ui/` and has an `adminui` realm for it.
7. **The JavaScript client is 0.6.0** (`npm install backd-js@latest`): `admin.whoami()`, `admin.config()`, `admin.data(database, collection)`, `admin.users.sessions()` and `revokeSession()`, the `q` search on `admin.users.list()` and the `origin` filter on `admin.jobs.list()`. It works with a v0.6.0 server for everything that existed before.

### Upgrading to v0.6.0

v0.6.0 adds [soft delete](../configuration/config-dir/#soft-delete), [admin rights per role](../auth/admin/#admin-rights), [`Prefer: respond-async`](../functions/calling/#asking-for-a-job-prefer-respond-async) and [`login_throttle`](../configuration/realm/#keys). Configuration that worked on v0.5.0 still loads and behaves the same; each new feature is off until you use it. What to check:

1. **Provision before rolling out**, as always. A collection with `soft_delete` gets its unique indexes rebuilt with `_meta.deleted_at` as the last key and, with a `retention`, a TTL index on `_meta.purge_at`; `PROVISION_MODE=verify` refuses to start until `backd provision` has created them. Collections that don't opt in are untouched.
2. **Turning soft delete on for a collection that already has a unique index:** the old index stays (`backd` never drops one) and also covers deleted documents, so a deleted document's values stay taken until you drop it. `backd provision` warns about it by name; drop it once the new index exists.
3. **Admin roles:** `admin: true` is what it was. A role can now list areas (`users`, `invitations`, `apikeys`, `secrets`, `audit`, `functions`) and an administrator can no longer hand out, or change a user who holds, rights they don't have themselves; admin API keys are created and revoked only by full administrators. Keep one role with `admin: true`: `backd bootstrap` needs it, and startup warns when a realm has none.
4. **`restore` and `purge` are new rule keys** for collections that soft-delete. They are denied unless declared and `write` doesn't cover them; declaring one in a collection without `soft_delete` stops startup. `backd rules test` fixtures take `restore` and `purge` assertions and `_meta.deleted_at`.
5. **Two query parameters are now meaningful:** `deleted` on list and get, and `purge` on `DELETE`. On a collection that doesn't soft-delete they still answer `400`, with a message that says so.
6. **`Prefer: respond-async` is per call.** Nothing changes for callers who don't send it. Browser apps on another origin can now send `Idempotency-Key` and `Prefer` and read `Preference-Applied` and `Idempotent-Replayed`: the CORS allow and expose lists lacked them.
7. **`login_throttle`** is optional; the defaults are the values the throttle always had.
8. **The JavaScript client is 0.5.0** (`npm install backd-js@latest`): `deleted`, `purge` and `restore` on collections, `respondAsync` on `fn()`. It works with a v0.5.0 server for everything that existed before.

### Upgrading to v0.5.0

v0.5.0 adds [cursor pagination](../api/querying/), [API key scopes](../auth/api-keys/), [session cookies](../auth/sessions/), [`Idempotency-Key` on creates](../api/documents/), [dates stored as dates](../configuration/config-dir/#dates) and `backd rules test`. Configuration that worked on v0.4.0 still loads; every new feature is off until you use it. What to check:

1. **Provision before rolling out**, as always: the `api_keys` collection of each realm's system database gains the `scopes` field in its validator, and each collection with a field marked `"x-backd-store": "date"` gets `bsonType: date` for it. `PROVISION_MODE=verify` refuses to start until `backd provision` has run.
2. **API key scopes are opt-in.** Existing keys have no scopes and keep full access. A key created with `scopes` can only do what they grant (`read`, `write` or `call`, on everything, a database or a collection or function), and answers `403` otherwise.
3. **`x-backd-store: date` does not convert existing data.** Documents written before keep text in that field until they are written again, and date queries skip them until then; the [dates section](../configuration/config-dir/#dates) has a `mongosh` one-liner to convert in one go.
4. **Session cookies are opt-in per realm and per login** (`sessions.cookie.enabled`, then `"cookie": true` on the login). A cookie login turns on an origin check for writes that carry only the cookie, so list your app's origin in `cors.origins`.
5. **`Idempotency-Key` on creates needs authentication.** A create or batch with the header answers `400` for anonymous callers and in realms with `auth: disabled`; without the header nothing changes. Answers are kept for 24 hours in the realm's existing `idempotency` collection.
6. **The admin user list is cut by MongoDB now** (`GET /_admin/users` returns `next_cursor`, with `after` to continue; `skip` still works). The command line and the JavaScript client already follow it; a script that read the whole list in one call must follow `next_cursor` for realms with more users than one page.
7. **Two limits are stricter, one is new.** The login throttle now serializes the password checks of an account across instances, and answers `429` with `Retry-After: 1` if it waits more than 5 seconds for the lock. `sessions.admin_idle_timeout` and `sessions.admin_max_lifetime` (unset: nothing changes) shorten the sessions of users who hold an admin role. See the [hardening checklist](checklist/).
8. **The JavaScript client is 0.4.0** (`npm install backd-js@latest`): `after` and `iterate` follow cursors, `cookies: true`, `scopes` when creating keys, and `idempotencyKey` on `create` and `batch` (keyed requests are retried by the client). It is versioned apart from the server, and works with a v0.4.0 server for everything that existed before.
9. **If you copied the template CI** (`backd template project`), add a step `backd rules test` (with `CONFIG_DIR: ./config`) after `backd config check`, as the template now does: it checks your access rules against the cases in each `rules.test.yaml`, with no database.

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
