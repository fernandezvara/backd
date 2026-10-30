---
title: "Operate"
description: "Running backd: MongoDB, deployment, providers, administration, monitoring, backup and hardening."
icon: "heartbeat"
weight: 400
toc: true
---

This section is for the **operator**: whoever deploys, secures, monitors, backs up and upgrades a `backd` service, and does not necessarily write the application on it. Building an application is [Build](../build/); changing backd itself is [Contribute](../contribute/); a message you do not recognize may be in [Troubleshooting](../troubleshooting/).

## Where to find things

| Task | Page |
|---|---|
| Deploy for real: TLS, MongoDB, rate limits | [Production deployment](production/) |
| Run it on Docker Compose or Kubernetes | [Docker Compose](production/docker-compose/), [Kubernetes](production/kubernetes/) |
| Build it on a cloud provider | [Cloud providers](production/cloud-providers/), starting with [DigitalOcean](production/cloud-providers/digitalocean/) |
| Run the functions executor, egress and workers | [Executor, workers and egress](functions-runtime/) |
| Package and promote the config | [Deploying config](deploying/) |
| Check before exposing it | [Hardening checklist](checklist/) and the [security model](administration/security/) |
| Run it: users, API keys, secrets, the audit trail | [Administration](administration/) and the [command line](administration/cli/) |
| Watch it | [Metrics](metrics/) and the [change feed](change-feed/) |
| Back it up and restore it | [Backup and restore](backup/) and [Keeping storage tidy](storage-maintenance/) |
| Connect email, storage and push providers | [Providers](providers/) |
| Something leaked | [If credentials leak](if-credentials-leak/) |
| Let companies sign in with their own Google or Microsoft account | [Organization sign-in](organization-sign-in/) |
| Move to a new version | *Upgrading to* below |

## MongoDB

`backd` needs MongoDB 8.2 running as a **replica set**. A single node is fine. Replica sets support transactions, which [atomic batch writes](../build/api/documents/#batch-writes) rely on, and every setup `backd` is tested with uses one. Sharded clusters (through `mongos`) work too.

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
- With authentication, a replica set also needs a key file (`--keyFile`), even with one node. The [production reference](production/docker-compose/#mongodb-with-authentication-and-tls) shows how.
- `make dev`, `make example` and the test setups start a single-node replica set and initiate it from the container's health check.

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
- Enables [pre-change images](change-feed/#requirements) (`changeStreamPreAndPostImages`) on collections that declare [`realtime: true`](../build/configuration/config-dir/#realtime). They are never switched off by a start: see [Disabling them](change-feed/#disabling-pre-change-images).
- Records the config's fingerprint for each realm (see [Deploying config](deploying/#what-backd-enforces)).
- Is idempotent and safe to run on every start.
- If a declared index exists with a different `unique` setting, `apply` stops with an error rather than dropping it. Drop the index manually and restart.
- Creating a unique index fails if the collection already holds duplicate values. The error names the index.

{{< hint style="tip" title="Already done for you" >}}
This is all done for you at startup: validators, declared indexes, the system databases and their TTL indexes. Nothing is ever **dropped**: a collection, index or database that exists in MongoDB but not in your config is kept and reported as a warning, so a config mistake can't delete data.
{{< /hint >}}

**`verify`**

- Only compares collections, validators, indexes and pre-change images with the config, including the system databases.
- If anything differs, it refuses to start and lists every difference. For example:

  ```
  backd serve: MongoDB does not match the config (run `backd provision`):
    shop__orders.items: collection missing
    shop__orders.clients: validator differs from schema.json
    shop__orders.clients: index (email) missing
    shop__orders.live: pre-change images are not enabled (this collection is realtime)
  ```

- Then compares the config's fingerprint with the one provisioning recorded for each realm, and refuses to start if it differs or none was recorded: every instance must run the config that was provisioned (see [Deploying config](deploying/#what-backd-enforces)).
- Needs only read/write privileges on the data, and read access to `backd___deployment.realms`.

**Common to both modes**

- Validators always use `validationLevel: "moderate"` and `validationAction: "error"`.
- **Nothing is ever dropped.** Some databases, collections and indexes exist in MongoDB but not in the config: databases named like `<realm>__<database>`, collections inside configured databases, or indexes not in `indexes.json`. `backd` doesn't serve those databases and collections, keeps those indexes, and logs a warning for each. The same goes for a `<realm>___system` database whose realm is gone or has `auth: disabled`: it is kept and reported, never used.
- Schema keywords that MongoDB can't enforce are logged at `info` level (see [Schema validation](../build/configuration/validation/)).

`backd provision` runs `apply` once and exits.

Both `backd provision` and `backd serve` then apply the [role assignments](../build/configuration/realm/#roles) seeded in each realm's `realm.yaml`.

### Realm system databases

Each realm with `auth: enabled` has a system database named `<realm>___system` (three underscores). It holds the realm's users and credentials, managed through the [admin API](administration/admin/) (for example with [`backd user`](../build/auth/users/)); only [`backd bootstrap`](administration/cli/#the-first-administrator) writes to it directly. Realm names must be short enough for this name to stay under MongoDB's 64-character limit, so a realm name can be at most 54 characters.

| Collection | Holds | Indexes |
|---|---|---|
| `users` | email, verified flag, roles, disabled flag | `email` (unique, sparse) |
| `identities` | sign-in methods per user: a password hash, or an external [provider](../build/auth/providers/)'s subject and address (and, for Apple, a sealed refresh token) | `provider` + `subject` (unique), `user_id` + `provider` (unique), `user_id` |
| `sessions` | sessions: public id, hashed token, last use and expiry | `token_hash` (unique), `user_id`, `expires_at` (TTL) |
| `api_keys` | hashed [API keys](../build/auth/api-keys/) with name, first characters, last use and expiry | `name` (unique) |
| `invitations` | [invitations](administration/admin/#invitations): public id, hashed token, optional email | `token_hash` (unique), `expires_at` (TTL) |
| `login_attempts` | failed-login counters per email and client address; reused, under their own key namespace, for [function rate limits](../build/functions/calling/#rate-limits) | `expires_at` (TTL) |
| `audit` | the [audit trail](administration/audit/): append-only records of security-sensitive actions | `at`, `target` + `at`, `expires_at` (TTL) |
| `secrets` | [function secrets](../build/functions/secrets/), encrypted at rest (never their plaintext values) | `database` + `name` (unique) |
| `invocations` | [function invocation history](../build/functions/logs/): append-only, what happened — never a call's input or output | `function` + `at`, `request_id` (sparse), `expires_at` (TTL) |
| `jobs` | [async function jobs](../build/functions/jobs/): input, status, and once done, result — the one exception to never storing input/output, since there's nowhere else to keep it until read | `status` + `lease_expires` + `created_at`, `expires_at` (TTL) |
| `oauth_states`, `login_codes` | the short-lived records of the [provider sign-in](../build/auth/providers/#the-flow): an attempt waiting for the provider, and a one-time login code (it records which sign-in method made it) waiting to be redeemed; both hashed, used once | `expires_at` (TTL) |
| `auth_challenges` | the steps between a first factor and a session: a [second factor](../build/auth/mfa/) to give; hashed, used once, 5 minutes | `expires_at` (TTL), `user_id` |
| `devices` | the [push devices](../build/push/devices/) users registered: the token sealed with the realm's key, looked up by its hash | `token_hash` (unique), `user_id`, `session_id`, `last_seen` |
| `mfa` | one document per user with a second factor: the TOTP secret (encrypted) and the recovery codes (keyed hashes) | `_id` is the user |
| `idempotency` | [`Idempotency-Key` claims](../build/functions/calling/#idempotency): scoped to the function and caller, a sync call's stored response or an async call's job id | `expires_at` (TTL) |

Each collection has a `$jsonSchema` validator that checks its required fields and their types. The TTL indexes make MongoDB delete expired sessions, invitations, counters, audit records, invocation records, jobs and idempotency claims automatically. If one of these indexes exists with different options (for example `expires_at` without TTL, so sessions would never expire), `apply` and `verify` fail and name it; drop the index and let `backd` recreate it.

Treat the system databases as sensitive: they contain password hashes and token hashes. Back them up with the rest of the realm's data, encrypted: only role seeds can be rebuilt from the config. Tokens and keys are stored only as hashes, so a leaked backup doesn't reveal usable credentials, but password hashes can still be attacked offline. See [Backup and restore](backup/), and [If credentials leak](if-credentials-leak/) for what to do if one does.

### Least-privilege deployments

The service's MongoDB user doesn't need to create collections, change validators or build indexes. Run provisioning as a separate deploy step instead:

1. Run `backd provision` with an admin-privileged `MONGO_URI`.
2. Grant the service's user `find`, `insert`, `update`, `remove` and `listIndexes` on each collection that `backd databases --collections` prints, and `listCollections` on their databases. MongoDB privileges can't match names by pattern, so repeat this step whenever the config changes.
3. Start the service with `PROVISION_MODE=verify` and that user.

{{< hint style="tip" title="Best practice" >}}
In production, run `backd provision` once per release with an admin URI and start every instance with `PROVISION_MODE=verify` and a user that can only read and write the configured collections. A compromised instance then can't change validators, indexes or collections.
{{< /hint >}}

Grant collections rather than MongoDB's built-in `readWrite` role on whole databases: `readWrite` also allows creating, dropping and re-indexing collections, and MongoDB lets `insert` on a database create any collection in it. The [production reference](production/docker-compose/#least-privilege-provisioning) automates all three steps.

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

### Upgrading to v0.9.6

v0.9.6 changes nothing in the data or the provisioned indexes: run `backd provision` as usual. What changes:

1. **Sizes follow one grammar.** A number and a case-sensitive unit with no space (`512KiB`, `1.5GB`); decimal units are powers of 1000, binary ones of 1024. A function's `memory` takes binary units only: `256MB` is refused, write `256MiB`. Run `backd config check` on your configuration before deploying. See [Sizes and units](../build/configuration/#sizes-and-units).
2. **`backd` and `backd executor` must run the same version.** The request to the executor carries `memory_bytes` instead of `memory_mb`: upgrade both together, or an older executor refuses the limit.
3. **Environment variables with a size** (`MAX_BODY_BYTES`, `BACKD_MAX_UPLOAD_BYTES`, `BACKD_REALTIME_MEMORY`) accept a unit as well as a number of bytes; existing values keep working.
4. **New clients.** `backd types --lang go` and `--lang python` generate types for the [Go client](../build/clients/go/) (`pkg/client`) and the [Python client](../build/clients/python/) (`backd` on PyPI). Existing languages are unchanged.
5. **Move the clients with the server.** `backd-js`, `package:backd` and the Python `backd` are released on the same version: `0.9.6` (for Dart, `backd: ^0.9.6`).

### Upgrading to v0.9.5

v0.9.5 adds [public files](../build/files/public-files/). Nothing changes for a realm that doesn't use them: the configuration, the data and the provisioned indexes stay as they are, and `backd provision` has nothing new to do. What changes:

1. **A file field can be `public: true`,** with a second, independent `public_storage:` in `realm.yaml` (its own provider, bucket, keys and the `url` browsers read from). A realm without `public_storage:` that declares a public field fails the configuration check. Read the [security notes](../build/files/security/) first: anyone with a public file's address can read it, and the bucket's public readability is yours to set up.
2. **Regenerate the types** (`backd types`, and `backd types --functions`): every file in a realm with files gains `public` and `key_name`, and a version gains `url`. Committed `types.d.ts`, `types.dart` and `backd.d.ts` files show a diff; a CI check with `--check` fails until they are regenerated.
3. **`backd storage check` and `reconcile` take `--storage private|public`** (private by default, as before), and `--direct` takes `--public-storage-key` and `--public-storage-secret`. The same choice exists as `?storage=public` / the `storage` body field in the admin API.
4. **Exports and imports carry public files.** An import checks each one like an upload (type, extension, name, metadata) before publishing it, and refuses a picture that stripping would change.
5. **`FileLink.expiresAt` is nullable in `package:backd`** (a public file's link never expires): handle `null` when you read it.
6. **Move the clients with the server.** `backd-js` and `package:backd` are released on the same version: `0.9.5` (for Dart, `backd: ^0.9.5`).

### Upgrading to v0.9.4

v0.9.4 changes nothing in the configuration, the data or the provisioned indexes: run `backd provision` as usual, there is nothing new for it. What changes:

1. **The admin UI has a new look.** Same pages and the same rights, with a top bar of grouped menus and a breadcrumb; the sign-in is one form with the realm as a field (links to `/_ui/r/<realm>/…` still open the same realm). See [the admin UI](administration/admin-ui/).
2. **A user whose password was expired for them alone can choose a new one signed out** with `POST /_auth/password/update` (and the admin UI's sign-in form offers it): the current password, and a code if the account has a second factor. A password expired for the **whole realm** (`expire-passwords`) cannot: it stays reset-by-email only, on purpose. See [Change the password signed out](../build/auth/sessions/#change-the-password-signed-out).
3. **`password_expired_at` goes back to `null` when the user gets a new password.** A script that reads it to find who is still expired now finds only those who are.
4. **The images carry Deno 2.9.8.** Running `backd` outside the images, install that Deno: a binary that finds another one says so.
5. **Move the clients with the server.** `backd-js` and `package:backd` are released on the same version: `0.9.4` (for Dart, `backd: ^0.9.4`).

### Upgrading to v0.9.3

v0.9.3 adds [organization sign-in](organization-sign-in/) (a company's own Google Workspace or Microsoft tenant) and scopes every session to one organization in a realm that has organizations (see [scoped sessions](../build/organizations/api/#signing-in-to-an-organization)). A realm without organizations changes nothing. Do these in order:

1. **Give every organization a slug.** `backd orgs list` shows `-` for the ones without; `backd orgs set-slug --id <org> --slug acme-corp` gives one (startup also warns about them). People sign in by the slug; a slug may not look like an id.
2. **Run `backd provision`** with an admin-privileged `MONGO_URI`. Sessions gain an index on `org` and the fields `max_lifetime`, jobs gain `caller_org` and API keys `org`; the `org_connections` collection is created, and the two unique indexes of `identities` become `(provider, org, subject)` and `(user_id, provider, org)` (nothing is rewritten, the old ones are dropped). With `PROVISION_MODE=verify` the instances refuse to start until it has run; with least-privilege grants, `backd databases --collections` prints the new collection.
3. **Move the apps to the organization sign-in.** Sign in at `/_auth/orgs/{org}/…` (`createClient({ org })` in the clients), drop `?org=`, `inOrg()`, `GET /_orgs` and the `/_orgs/invitations/*` calls, and make the sign-up page ask for the team's slug. [The sign-in experience](../build/organizations/sign-in-experience/) describes what to build. Realm administrators keep `POST /_auth/login`; the admin UI and `backd login` need nothing.
4. **Expect everyone to sign in once more.** Sessions opened before have no organization and answer `401 session_unscoped`; the clients take that as an expired session. A session of a user who is an administrator keeps working.
5. **Bind the keys that act on behalf of users.** `backd apikey create --org <org>` makes a key that counts one organization; the ones that exist count none until replaced.
6. **Organization sign-in is off until you turn it on** (`organizations.sso` in `realm.yaml`, a role with `manage: sso`, `BACKD_SECRETS_KEY` and a public address). See [Organization sign-in for operators](organization-sign-in/).
7. **The images keep `backd` at `/usr/local/bin/backd`** (the plain `backd` image used to have it at `/backd`), with `/backd` a link to it, so an `entrypoint:` or `exec` that names either keeps working.
8. **Move the clients with the server.** `backd-js` and `package:backd` are released on the same version: `0.9.3` (for Dart, `backd: ^0.9.3`).

### Upgrading to v0.9.2

v0.9.2 changes where a realm keeps its content (breaking, step 1) and adds the personal data export and live app configuration. Run `backd config check` against your tree with the new binary first: it lists every folder to move.

1. **Move the realm's content folders into `_realm/`.** `email`, `pages`, `push`, `consents`, `compliance` and `app-config` leave the top of each realm for `<realm>/_realm/` (`app-config` becomes `_realm/config`); `realm.yaml` and the databases stay where they are. For a realm called `acme`: `mkdir acme/_realm && mv acme/email acme/pages acme/push acme/consents acme/compliance acme/_realm/` (only the ones you have), and `mv acme/app-config acme/_realm/config`. The old layout stops loading, and the error names each folder and where it goes; `backd config check` shows it before you deploy. The [config fingerprint](deploying/) changes, so every instance must run the new tree.
2. **Run `backd provision`** with an admin-privileged `MONGO_URI`. It creates the `user_exports` system collection (the records of [personal data exports](../build/auth/personal-data/)). With `PROVISION_MODE=verify` the instances refuse to start until it has run. If you run with least-privilege grants, `backd databases --collections` prints the new collection.
3. **Nothing else changes until you turn exports on** (`compliance.export: true`, which needs `storage:` and `email:`). `data-export-ready` and `data-export-made` are built-in email kinds; `backd template realm` writes the English text and a `_realm/compliance/readme/en.html` to edit.
4. **Move the clients with the server.** `backd-js` and `package:backd` are released on the same version: `0.9.2` (for Dart, `backd: ^0.9.2`).

### Upgrading to v0.9.1

v0.9.1 adds [field encryption](../build/encryption/), breach-response tools (password expiry, ending every session, one-time reset links) and export and import with anonymized copies for staging. Nothing breaks for existing configuration, but the release adds system collections and validators, so it needs `backd provision`. What to check:

1. **Run `backd provision`** with an admin-privileged `MONGO_URI`. It creates the `data_keys` and `security_state` system collections and updates the validators of the existing ones. With `PROVISION_MODE=verify` the instances refuse to start until it has run.
2. **Field encryption and password expiry are off until you use them.** A field is encrypted when `collection.yaml` gives it `fields.<path>.encrypt.key`; create the key first with `backd secret create` (it is shown once: save it).
3. **Grant the service's user the new collections** if you run with least-privilege grants: `backd databases --collections` prints them with the others.
4. **Run `backd types` again** to get `redacted` in the generated TypeScript types; `backd types --lang dart` does the same for Dart.
5. **The Dart client moves to the release train.** `package:backd` 0.9.1 goes with `backd` v0.9.1 and `backd-js` 0.9.1; it was `0.0.5`, and a caret on a `0.0.x` version never picks a newer one, so change `backd: ^0.0.5` to `backd: ^0.9.1`.
6. **Export and import are stricter.** An import checks the paths and checksums of every part of a package and holds `_meta` and file sizes to the fields' rules; a package that was edited by hand may now be refused.

### Upgrading to v0.9.0

v0.9.0 adds the [Dart client](../build/clients/dart/) and the generators around it. The server changes only add: no new collections, no new configuration, nothing to provision. What to check:

1. **Nothing breaks, and nothing needs provisioning.** If you run `PROVISION_MODE=verify`, no `backd provision` run is needed for this release alone.
2. **`backd types --lang dart`** writes a realm's `types.dart` (a `ShelfRealm` accessor and document, input, `where`/`orderBy`, function and configuration classes) next to the TypeScript output, and the admin types endpoint takes `?lang=dart`. `backd openapi` keeps feeding openapi-generator for Kotlin, Swift and the rest; `make openapi-clients` builds all three from the example realms' documents.
3. **The Dart client is `package:backd` on pub.dev** (`dart pub add backd`), versioned on its own: sign-in with the second-factor, magic-link, provider and consent steps, collections, files with direct uploads and image versions, function jobs, live lists over realtime, push devices, app configuration, organizations and generated realm types (`BackdClient<ShelfRealm>`). It has **no admin API, by design**: administration stays in the JavaScript client, the admin UI and the CLI.

### Upgrading to v0.8.5

v0.8.5 adds [realtime subscriptions](../build/api/realtime/) and [triggers](../build/functions/triggers/), both built on MongoDB's change stream. Nothing is on until a collection declares `realtime:` or `triggers:`, and nothing breaks for config that doesn't. What to check:

1. **Run `backd provision` when you declare `realtime:` or `triggers:`,** before you start instances with `PROVISION_MODE=verify`. Provision enables **pre-change images** on those collections (without them a removal can't be told to the right subscribers, and `verify` refuses to start, naming the collection) and creates the new `jobs` indexes the trigger dispatcher uses (the per-realm backlog count, and `serial_key`/`serial_order` for ordered triggers). Dropping `realtime:` from a collection leaves its images in place; `backd provision --disable-preimages` removes them.
2. **Realtime needs MongoDB 6.0 or later as a replica set,** which the change feed requires at startup. The stream is opened on the whole deployment, so the `MONGO_URI` user needs the `changeStream` and `find` actions on every database — for example the built-in `readAnyDatabase` role; a confined user that can't get it can't serve realtime. See the [change feed requirements](change-feed/#requirements).
3. **Split the work per instance.** `BACKD_EVENTS=false` keeps realtime streams off request-serving replicas; `BACKD_TRIGGERS=false` keeps an instance from holding the trigger dispatcher's lease. Both default to `true`, and nothing is read until a collection declares the feature.
4. **A trigger's writes are stamped `_meta.chain`** (`{root, depth}`) so chains can be bounded (`triggers.max_chain`, `max_chained_per_minute`, `max_backlog` in `realm.yaml`). A client can't set it and a function can't drop it. `backd triggers status|gaps|resolve` and `backd triggers rescan` inspect and repair the dispatcher.
5. **The JavaScript client is 0.8.5** (`npm install backd-js@latest`): `client.realtime.subscribe()` and `collection.subscribe()` live lists, the generated trigger input types and four typed errors (`RealtimeNotEnabledError`, `TooManySubscriptionsError`, `InvalidResumeTokenError`, `SessionEndedError`). It keeps working with a v0.8.4 server for what existed there; live lists need a v0.8.5 server.

### Upgrading to v0.8.4

v0.8.4 adds reference expansion, aggregations and full-text search. Nothing is on until a collection declares it, and nothing breaks for config that doesn't. What to check:

1. **Run `backd provision` when you declare a text index or the indexes an aggregation's warning asks for,** before you start instances with `PROVISION_MODE=verify`. A text index is created (and rebuilt when its declaration changes, with searches answering `503 index_building` meanwhile); a collection with a `language_field` also gets a server-owned `_search_lang` in its validator and, once, in the documents that already exist.
2. **Two names are now reserved in `schema.json`: `_expanded` and `_search_lang`.** A schema that declares a property with either name stops `backd` at startup, with the file named: rename the property.
3. **New, opt-in features:** [`references:`](../build/api/references/) in `collection.yaml` and `?expand=` on reads (`BACKD_EXPAND_MAX_DEPTH`, `_PATHS`, `_ARRAY`, `_DOCS`), [`aggregate:`](../build/configuration/config-dir/#aggregations) and `GET …/_aggregate` (`BACKD_AGGREGATE_TIME`, `BACKD_AGGREGATE_MAX_GROUPS`), a [text index](../build/configuration/config-dir/#a-text-index) and `$search`. `backd` logs a warning at startup for each `group_by` field that no index starts with.
4. **`_batch` still only writes,** so there is nothing to expand there; `ctx.db` and `ctx.admin.db` in functions do expand, aggregate and search.
5. **The JavaScript client is 0.8.4** (`npm install backd-js@latest`): `expand()`, `aggregate()`, `search()`, `fillBuckets()` and typed `_expanded`. It keeps working with a v0.8.3 server for what existed there; the new calls need a v0.8.4 server. Types generated by an older `backd types` keep working; regenerate to get the new ones.

### Upgrading to v0.8.3

v0.8.3 adds [organizations](../build/organizations/) and team access. It has no breaking changes: everything is off until `realm.yaml` declares `organizations`. What to check:

1. **Run `backd provision` before you start instances with `PROVISION_MODE=verify`.** The release adds the system collections `orgs`, `org_members`, `org_teams`, `org_invitations`, `org_member_seq` and `org_templates`, and indexes on collections that declare `org` or `team`. `apply` creates them at startup; `verify` refuses to start until a provisioning run did, and the least-privilege user of the [production reference](#provisioning) needs them granted.
2. **Organizations are opt-in.** Declare `organizations` in `realm.yaml` (roles, `team_roles`, `hooks`, `require_mfa`) and `organization: required|optional` and `team: required|optional` in `collection.yaml`. `_meta.org` and `_meta.team` are server-owned. See [organizations](../build/organizations/) and the [rules cookbook](../build/organizations/rules-cookbook/).
3. **Rules, functions and the client gain surface:** `inOrg`, `hasOrgRole`, `inTeam` and `hasTeamRole` in rules, `ctx.user.orgs` and `ctx.admin.orgs` in functions, and organizations in the JavaScript client and its admin API. Existing rules keep working.
4. **The JavaScript client is 0.8.3** (`npm install backd-js@latest`). It keeps working with a v0.8.2 server for what existed there; the organizations methods need a v0.8.3 server.

### Upgrading to v0.8.2

v0.8.2 is a large minor release, and it has breaking changes, as allowed before a stable release: the first five points need your action, the rest are worth a look. Features are off until your config uses them. What to check:

1. **Run `backd provision` before you start instances with `PROVISION_MODE=verify`.** The release adds system collections (`oauth_states`, `login_codes`, `auth_challenges`, `mfa`, `devices`, `consent_records`, `app_config`, `app_config_history`, `push_sends`) and one index on every data collection, `_meta.created_at,id`, which serves the new default order of lists. `apply` creates them at startup; `verify` refuses to start until a provisioning run did, and the least-privilege user of the [production reference](#provisioning) needs the new collections granted. The index is built once per collection: on a very large collection do it at a quiet time.
2. **Lists without `order_by` are ordered by `_meta.created_at`, then `id`** (before: by `id`). For generated ids that is almost the same; an `after` cursor is now an opaque value, and the bare id works only with `order_by=id`. Callers that built `after=<id>` by hand must add `order_by=id`. See [Querying](../build/api/querying/#ordering-and-pagination).
3. **A create that sends `id` to a collection that generates ids is `400 id_not_allowed`** (before, the field was ignored). Collections that declare [`custom_id`](../build/configuration/custom-ids/) require it.
4. **Sensitive account actions ask for a fresh proof of identity, and their `password` fields are gone.** Changing the password or the address, deactivating the account, unlinking a sign-in method, turning the second factor on or off, and creating an API key as an administrator signed in with a session answer `401 reauth_required` without an `X-Backd-Reauth` token from `POST /_auth/reauth`. The JavaScript client and the admin UI do it for you; direct callers of those endpoints must change. See [Fresh proof](../build/auth/reauth/).
5. **`email.default_locale` and `email.locales` moved to the top level of `realm.yaml`** (`default_locale`, `locales`), shared by email and push. The old keys are refused at startup, with a message that says where they went. The sign-in return parameters of provider flows are now prefixed `backd_` (`backd_code`, `backd_error`, `backd_linked`): a page that handles the return by hand must read the new names (`backd-js` does it for you).
6. **New, opt-in features:** [sign-in with Google, Microsoft or Apple](../build/auth/providers/), [a second factor](../build/auth/mfa/) (`mfa:` in `realm.yaml`; the template's admin role requires one, so administrators enrol on their next sign-in), [magic links and accounts without a password](../build/auth/magic-links/), [push notifications](../build/push/), [consents](../build/auth/consents/) and a [personal data scope](administration/compliance/), [app configuration](../build/configuration/app-config/), [conditional GET and public caching](../build/api/caching/), [custom ids](../build/configuration/custom-ids/), [generated TypeScript types](../build/clients/types/) and a [per-realm OpenAPI document](../build/api/openapi/). Several of them seal values with the key of `BACKD_SECRETS_KEY`: set it before you turn them on.
7. **`BACKD_DEV=true` reloads the whole config live** (schemas, rules, indexes, functions and templates), and rewrites `backd.d.ts` in each functions folder. Production behavior is unchanged.
8. **The JavaScript client is 0.8.2**, on the same version as the server from now on (`npm install backd-js@latest`). It adds the second step and magic links, provider sign-in, push, consents, `client.config`, the cache, typed clients (`createClient<Realm>()`) and the custom-id errors. It keeps working with a v0.8.1 server for what existed there.

### Upgrading to v0.8.1

v0.8.1 adds [image versions](../build/files/file-fields/#image-versions), [storage quotas](providers/storage/#quotas) and [direct uploads in parts](../build/files/transfers/#files-above-5-gib), and it has breaking changes: the first two points need your action, the rest are worth a look. Nothing else that worked on v0.8.0 changes. What to check:

1. **Move `rules.yaml` into `collection.yaml`.** The rules are now its `rules:` section (same keys). A collection directory that still has a `rules.yaml` **does not load**, so a forgotten file can't leave a collection without rules: indent the file's content two spaces under `rules:` in `collection.yaml` and delete it. `rules.test.yaml` stays where it is. See [rules](../build/auth/rules/).
2. **`backd` without a command no longer serves.** It prints its help and exits 0. The images already run `serve`; a Dockerfile, compose file or unit that started `backd` alone must say `backd serve`.
3. **JPEG and PNG originals lose their metadata by default** (EXIF with the GPS position, XMP, comments, PNG text), pixels untouched and orientation kept. Set `keep_metadata: true` on a field to keep it. A picture too damaged to be copied is refused with `422 invalid_image`. Files stored before the upgrade are not changed. See [Metadata and the GPS risk](../build/files/file-fields/#metadata-and-the-gps-risk).
4. **Image versions and quotas are opt-in:** `versions:` under a file field (workers make the copies, so run them; `?version=` serves them and `backd files regenerate` remakes them after a declaration changes) and `storage.quota` in `realm.yaml`.
5. **Direct uploads above 5 GiB** need a provider that checks a SHA-256 per part: AWS S3, Cloudflare R2 and MinIO. With DigitalOcean Spaces `max_size` of a direct field stays at 5 GiB. A file sent in parts has a `sha256` that is the digest of its parts' digests, not of its bytes ([why](../build/files/transfers/#files-above-5-gib)). Workers abort the uploads left unfinished.
6. **Sizes accept `TB` and `TiB`.** `GET …/_files/{field}` adds `multipart_above` for the fields that can hold more; the start of a direct upload takes `part_sha256` instead of `sha256` for such a file.
7. **API additions:** `POST /_admin/config/check` (a change checked against the realm's whole configuration, without applying it) and the image version routes. The admin UI's schema and rules editors use the first.
8. **The JavaScript client is 0.8.0** (`npm install backd-js@latest`): `uploadFile` sends large files in parts by itself, plus the version helpers and `admin.checkConfig()`. It works with a v0.8.0 server for everything that existed before.

### Upgrading to v0.8.0

v0.8.0 adds [files](../build/files/), [schema checks](../build/configuration/validation/#finding-documents-that-no-longer-match), [`on_complete`](../build/functions/jobs/) and [job steps](../build/functions/jobs/), and more control over [schedules](../build/functions/cron/) (pausing, `timezone`, `overlap: skip`). Configuration that worked on v0.7.0 still loads, except for the one case in point 2; each new feature is off until you use it. What to check:

1. **Provision before rolling out**, as always. New system collections are created in each realm's `___system` database: `file_journal`, `file_deletions`, `storage_usage`, `schema_checks` and `schedules`. `PROVISION_MODE=verify` refuses to start until `backd provision` has made them.
2. **A file field can't be declared in `schema.json`.** Files are declared under `files:` in `collection.yaml` (backd adds their schema), and startup refuses a collection that has the same property in both, and one with `files:` in a realm without `storage:`. If you used a property to hold file details, move it. The tutorial's `assets.file` placeholder moved this way.
3. **Files are opt-in, and need more than configuration.** A realm declares [`storage:`](providers/storage/) in `realm.yaml` and sets its two keys as realm secrets (`backd secret set`, with `BACKD_SECRETS_KEY` on the instance); without them the realm's file endpoints answer `503 storage_unavailable` and reads of its documents come without links. **Run workers** (`backd worker`, or `--with-worker`): they delete the objects of replaced, removed and abandoned files and finish uploads a crash interrupted. Set **`BACKD_URL`** in production, since backd-signed links name it. `BACKD_MAX_UPLOAD_BYTES` (default 100 MiB) caps proxy uploads.
4. **Give each backd instance its own storage `prefix`**, and read [Security of files](../build/files/security/): links and caches outlive later rule changes, and a function can't declare the storage secrets or `BACKD_FILES_LINK_KEY` as its own (startup refuses it).
5. **Erasing a user** now also queues the files of the documents the policy deletes or anonymizes (a `remove` of a file field). A `DELETE`, a batch delete and a purge queue the document's files too. MongoDB's own retention of a soft-deleted document can't: [`backd storage reconcile`](storage-maintenance/#reconciling) finds those objects.
6. **The admin UI** gains the file controls and schema checks. Its `Content-Security-Policy` now also allows images and direct uploads from the origin of the realms' file storage (and from no other origin besides its own); a stack without storage has the policy it had.
7. **API and CLI additions:** the `_files` routes and `?file_links=true` on reads; `GET /_admin/storage`, `POST /_admin/storage/check` and `/reconcile`; `/_admin/data-checks`; `/_admin/schedules`; `backd storage check|usage|reconcile`, `backd data check|checks`, `backd functions schedules|pause|resume`. New error codes: `too_many_files`, `upload_mode_mismatch`, `unsupported_file_type`, `file_missing`, `invalid_file_link`, `upload_mismatch`, `file_not_uploaded` and `storage_unavailable`. The `owned` report lists `replace` as `[]`, no longer `null`, for a policy that replaces nothing.
8. **The tutorial's stack** now runs a MinIO: fetch `compose.yaml` and `nginx.conf` again before chapter 13.
9. **The JavaScript client is 0.7.0** (`npm install backd-js@latest`): `files(id, field)`, `uploadFile`, `prepareUpload`, `fileUrl`, `downloadFile`, `deleteFile`, `fileLinks`, `admin.storage.status()` and `reconcile()`, `admin.dataChecks`, `admin.schedules`, `job.steps` and `wait({ onProgress })`. It works with a v0.7.0 server for everything that existed before.

### Upgrading to v0.7.0

v0.7.0 adds the [admin web interface](administration/admin-ui/), a [read-only admin level](administration/admin/#the-read-only-level) with `admin.read_access`, an audited [admin data route](administration/admin/#data), a read-only [configuration view](administration/admin/#configuration) in the admin API, and a switch that turns the admin API off per instance. Configuration that worked on v0.6.0 still loads; the interface is off until you switch it on. What to check:

1. **`admin: true` and admin API keys now reach documents.** The new [admin data route](administration/admin/#data) (`/v1/{realm}/_admin/data/…`) lists, reads and changes documents **past the collection's access rules**, and it belongs to every `admin: true` role and every admin API key. Writes are audited without their content; reads are not (they carry the actor in the access log). Before upgrading, look at who holds those roles and keys: give people who don't need documents an area list (`admin: [users, invitations]`), which doesn't include the new `data` area, and keep admin keys to the tooling that needs them.
2. **Two new areas.** An area list can now also name `data` and `config`; a role that lists areas and doesn't name them doesn't get them. `config` shows what the instance runs for the realm (`GET /_admin/config`) and every read-only level has it.
3. **The read-only level is new.** `admin: read` (or `[read, secrets]`) reads every area except users and data, which a realm opts in to with `admin.read_access.users` and `admin.read_access.data` (both `false` by default). `GET /_admin/whoami` tells a client what the signed-in level may do.
4. **The admin API can be switched off per instance:** `BACKD_ADMIN_API=false` leaves every `/_admin` route out (`404`). The [production reference](production/docker-compose/#admin-api) now splits a public `backd` (no admin API) from an internal `backd-admin`, and the CLI's `BACKD_URL` points at the internal one: if you built on `deploy/production`, compare your compose file with it.
5. **The admin web interface is off by default.** `BACKD_ADMIN_UI=true` serves it at `/_ui/` from an instance that also has the admin API (the combination with `BACKD_ADMIN_API=false` stops startup); `BACKD_ADMIN_UI_IDLE` (default `30m`) signs an idle administrator out. Release images and binaries embed it; a binary built from source needs `make ui` (Node) first, and without it `/_ui/` answers `404` and startup warns. Serve it only from the internal instance, and read its [security review](administration/admin-ui/#security-review) and the [hardening checklist](checklist/) first.
6. **Smaller changes:** `GET /_admin/users` takes `q` (a search of at most 254 characters), `GET /_admin/users/{id}/sessions` and `DELETE …/sessions/{session_id}` list and end a user's sessions. The example stack serves `/_ui/` and has an `adminui` realm for it.
7. **The JavaScript client is 0.6.0** (`npm install backd-js@latest`): `admin.whoami()`, `admin.config()`, `admin.data(database, collection)`, `admin.users.sessions()` and `revokeSession()`, the `q` search on `admin.users.list()` and the `origin` filter on `admin.jobs.list()`. It works with a v0.6.0 server for everything that existed before.

### Upgrading to v0.6.0

v0.6.0 adds [soft delete](../build/configuration/config-dir/#soft-delete), [admin rights per role](administration/admin/#admin-rights), [`Prefer: respond-async`](../build/functions/calling/#asking-for-a-job-prefer-respond-async) and [`login_throttle`](../build/configuration/realm/#keys). Configuration that worked on v0.5.0 still loads and behaves the same; each new feature is off until you use it. What to check:

1. **Provision before rolling out**, as always. A collection with `soft_delete` gets its unique indexes rebuilt with `_meta.deleted_at` as the last key and, with a `retention`, a TTL index on `_meta.purge_at`; `PROVISION_MODE=verify` refuses to start until `backd provision` has created them. Collections that don't opt in are untouched.
2. **Turning soft delete on for a collection that already has a unique index:** the old index stays (`backd` never drops one) and also covers deleted documents, so a deleted document's values stay taken until you drop it. `backd provision` warns about it by name; drop it once the new index exists.
3. **Admin roles:** `admin: true` is what it was. A role can now list areas (`users`, `invitations`, `apikeys`, `secrets`, `audit`, `functions`) and an administrator can no longer hand out, or change a user who holds, rights they don't have themselves; admin API keys are created and revoked only by full administrators. Keep one role with `admin: true`: `backd bootstrap` needs it, and startup warns when a realm has none.
4. **`restore` and `purge` are new rule keys** for collections that soft-delete. They are denied unless declared and `write` doesn't cover them; declaring one in a collection without `soft_delete` stops startup. `backd rules test` fixtures take `restore` and `purge` assertions and `_meta.deleted_at`.
5. **Two query parameters are now meaningful:** `deleted` on list and get, and `purge` on `DELETE`. On a collection that doesn't soft-delete they still answer `400`, with a message that says so.
6. **`Prefer: respond-async` is per call.** Nothing changes for callers who don't send it. Browser apps on another origin can now send `Idempotency-Key` and `Prefer` and read `Preference-Applied` and `Idempotent-Replayed`: the CORS allow and expose lists lacked them.
7. **`login_throttle`** is optional; the defaults are the values the throttle always had.
8. **The JavaScript client is 0.5.0** (`npm install backd-js@latest`): `deleted`, `purge` and `restore` on collections, `respondAsync` on `fn()`. It works with a v0.5.0 server for everything that existed before.

### Upgrading to v0.5.0

v0.5.0 adds [cursor pagination](../build/api/querying/), [API key scopes](../build/auth/api-keys/), [session cookies](../build/auth/sessions/), [`Idempotency-Key` on creates](../build/api/documents/), [dates stored as dates](../build/configuration/config-dir/#dates) and `backd rules test`. Configuration that worked on v0.4.0 still loads; every new feature is off until you use it. What to check:

1. **Provision before rolling out**, as always: the `api_keys` collection of each realm's system database gains the `scopes` field in its validator, and each collection with a field marked `"x-backd-store": "date"` gets `bsonType: date` for it. `PROVISION_MODE=verify` refuses to start until `backd provision` has run.
2. **API key scopes are opt-in.** Existing keys have no scopes and keep full access. A key created with `scopes` can only do what they grant (`read`, `write` or `call`, on everything, a database or a collection or function), and answers `403` otherwise.
3. **`x-backd-store: date` does not convert existing data.** Documents written before keep text in that field until they are written again, and date queries skip them until then; the [dates section](../build/configuration/config-dir/#dates) has a `mongosh` one-liner to convert in one go.
4. **Session cookies are opt-in per realm and per login** (`sessions.cookie.enabled`, then `"cookie": true` on the login). A cookie login turns on an origin check for writes that carry only the cookie, so list your app's origin in `cors.origins`.
5. **`Idempotency-Key` on creates needs authentication.** A create or batch with the header answers `400` for anonymous callers and in realms with `auth: disabled`; without the header nothing changes. Answers are kept for 24 hours in the realm's existing `idempotency` collection.
6. **The admin user list is cut by MongoDB now** (`GET /_admin/users` returns `next_cursor`, with `after` to continue; `skip` still works). The command line and the JavaScript client already follow it; a script that read the whole list in one call must follow `next_cursor` for realms with more users than one page.
7. **Two limits are stricter, one is new.** The login throttle now serializes the password checks of an account across instances, and answers `429` with `Retry-After: 1` if it waits more than 5 seconds for the lock. `sessions.admin_idle_timeout` and `sessions.admin_max_lifetime` (unset: nothing changes) shorten the sessions of users who hold an admin role. See the [hardening checklist](checklist/).
8. **The JavaScript client is 0.4.0** (`npm install backd-js@latest`): `after` and `iterate` follow cursors, `cookies: true`, `scopes` when creating keys, and `idempotencyKey` on `create` and `batch` (keyed requests are retried by the client). It is versioned apart from the server, and works with a v0.4.0 server for everything that existed before.
9. **If you copied the template workflow** (`backd template project` → `.github/workflows/backd.yml`), add a step `backd rules test` (with `CONFIG_DIR: ./config`) after `backd config check`, as the template now does: it checks your access rules against the cases in each `rules.test.yaml`, with no database.

### Upgrading to v0.4.0

v0.4.0 adds [email flows](../build/functions/email/), [erasure](administration/erasure/), [metrics](metrics/) and the npm client. Configuration that worked on v0.3.0 still loads and nothing new is required, but two behaviors change, so read the first two items.

1. **Deleting a user means something else.** `DELETE /_auth/me` (a user deleting their own account) now **deactivates** it: the account is disabled and its sessions end, but the data and the email address are kept, and an administrator can reactivate it. `DELETE /_admin/users/{id}` and `backd user delete` now **erase**: the user becomes a tombstone (`erased-<id>@erased.invalid`, so the real address is free again) and a worker applies the `collection.yaml` policy of each collection that declares one; they answer `202` with an erase job, and `backd user delete` waits for it. Before, both removed the user and left every document alone. To get that effect now, declare no policy: an erase without policies leaves the documents as they are. A worker must be running (`backd worker`, or `serve --with-worker`); see [Deleting and erasing users](administration/erasure/).
2. **Provision before rolling out**, as always: each realm's system database gains collections and indexes (email tokens, an index of erase jobs), and `PROVISION_MODE=verify` refuses to start without them. The worker now claims jobs in every realm with authentication, not only those with functions.
3. **Some addresses are refused.** Addresses on the reserved `.invalid` domain, with a comma, semicolon, colon, angle bracket, parenthesis, square bracket, quote or backslash, or longer than 254 characters no longer pass, at sign-up and when looked up. An existing account with such an address can't sign in by email until an administrator changes the address in the database. Ordinary addresses are not affected.
4. **If you copied the [production reference](production/)**: the stricter rate-limit zone now covers password reset, verification, invitations and address changes (`nginx/backd.conf.template`), and it gained a Prometheus. Run its `setup.sh` again: it adds `BACKD_METRICS_TOKEN` to your `.env` and `secrets/metrics-token`.
5. **The JavaScript client is on npm as `backd-js`** (`npm install backd-js`). It was never published as `@backd/client`: if you installed it from a checkout, change the import. Versions of `backd-js` before 0.2 were an unrelated, older library.
6. **Metrics, email and the `account` settings are all off by default.** Turn them on when you want them: `METRICS_ADDR`, the `email` section of `realm.yaml` and `account.*`.

### Upgrading to v0.2.0

v0.2.0 adds users and access rules and is a breaking release: every realm needs a [`realm.yaml`](../build/configuration/realm/), and authentication is on by default. To keep an existing realm working as before on a private network, give it a `realm.yaml` with `auth: disabled`; to protect it, provision it and give server-side clients [API keys](../build/auth/api-keys/).

1. **Add a `realm.yaml` to every realm.** To keep a realm working exactly as before, without authentication, put `auth: disabled` in it. Only do this for realms that stay on a private network: anyone who can reach them can read and modify their data.
2. **Or turn authentication on** (the default, `auth: enabled`), declare an admin role in `realm.yaml` (`roles: {admin: {admin: true}}`), then:
   - run `backd provision` (or start `backd serve` with `PROVISION_MODE=apply`) to create the realm's system database;
   - create the realm's first administrator with `backd bootstrap --realm <realm> --email <email>`, and log in as them with `backd login --realm <realm> --url <server>`;
   - give server-side clients an API key: `backd apikey create --realm <realm> --name <name> --expires 90d`, sent as `Authorization: Bearer <key>`;
   - add a `rules:` section to the `collection.yaml` of collections that users or anonymous callers should reach. Without one, only API keys can.
3. **Existing documents** don't have ownership fields. They keep working: `_meta.owner` reads as `null`, so owner-based rules don't match them, and their next write adds `_meta.updated_by`. API keys can still manage them.
4. **Behind a reverse proxy**, set `TRUSTED_PROXIES` so login throttling sees real client addresses.
5. **Browser apps** on another origin need that origin under `cors.origins` in `realm.yaml`.

## Health checks

Use `GET /healthz` as the liveness probe and `GET /readyz` as the readiness probe (see [HTTP API](../build/api/#health-endpoints)).

## Logging

Logs are JSON lines on standard error, filtered by `LOG_LEVEL`.

- At startup, `backd` logs a `WARN` line for each realm with `auth: disabled` (its data is open to anyone who can reach the service), and for each realm with `auth: enabled` but no rules in any collection (only API keys can reach its data). It warns about realms with `auth: enabled` that nobody can administer with a session (no admin role, or no user holding one). It also lists API keys that never expire or expire within 14 days (see the [expiry policy](../build/auth/api-keys/#expiry-and-rotation-policy)).
- Each request produces one access-log line (`"msg":"request"`). It includes the method, route pattern, realm, database, collection, status, duration, bytes, `request_id` and the client's address (`client`, following [`TRUSTED_PROXIES`](#client-addresses-behind-a-proxy)), plus `actor` in realms with `auth: enabled`: `user:<id>` for a session, `key:<name>` for an API key, `key:<name> as user:<id>` for an API key acting on behalf of a user, or `anonymous`.
- At `debug` level, requests refused by [access rules](../build/auth/rules/#answers-when-a-rule-says-no) are logged (`"msg":"access denied"`) with the collection, operation and reason.
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

`backd` serves plain HTTP. Terminate TLS in front of it with a reverse proxy, ingress or load balancer. The [production reference deployment](production/) does this and everything else below. Before going public, go through the [hardening checklist](checklist/). Before exposing a realm, also go through the [security checklist](administration/security/#checklist-before-exposing-a-realm).

### Rate limiting and abuse

`backd` doesn't limit request rates, except for [failed logins](../build/auth/sessions/#brute-force-protection). Put a reverse proxy, API gateway or CDN in front of it and limit there (the [production reference](production/docker-compose/#rate-limits) has a tested nginx configuration):

- **Sign-up:** in realms with `signup: open`, limit `POST /v1/{realm}/_auth/signup` per client address, or use `signup: invite`.
- **Public data:** collections whose `read` rule lets anonymous callers in can be scraped; limit or cache them.
- **Everything else:** a general per-address limit protects MongoDB from floods.

Password hashing is bounded by `PASSWORD_HASH_CONCURRENCY` whatever the traffic: excess logins queue, then answer `503` at their deadline, instead of exhausting memory.

### Client addresses behind a proxy

[Login throttling](../build/auth/sessions/#brute-force-protection) counts failures per client address. Behind a proxy, every connection comes from the proxy, so set `TRUSTED_PROXIES` to the proxies' exact addresses:

```sh
TRUSTED_PROXIES=172.30.80.10
```

List networks only if every address in them is one of your proxies. Whole private ranges such as `10.0.0.0/8` would let any other container or host in them forge `X-Forwarded-For` (see [client addresses](production/docker-compose/#client-addresses) in the production reference).

- `X-Forwarded-For` is only believed when the connection comes from a trusted proxy. Otherwise clients could pick any address they like.
- The client is the right-most address in `X-Forwarded-For` that isn't a trusted proxy. Entries further left were supplied by the client and are ignored.
- The proxy in front of `backd` must overwrite `X-Forwarded-For` with the address it sees, unless it trusts a load balancer in front of it. The [production reference](production/docker-compose/#client-addresses) shows how. The local stacks (`make dev`, `make example`) trust all private ranges for convenience: don't copy that.
- Without `TRUSTED_PROXIES`, `backd` uses the connection's address. Behind a proxy, all clients then share one counter, and a flood of failures from anyone slows down logins for everyone behind that proxy.
