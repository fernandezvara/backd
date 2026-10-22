# Changelog

All notable changes to `backd` and to its JavaScript client `backd-js`, which are released on the same version from 0.8.2. The upgrade notes, with what needs your action, are in the documentation under [Operations → Upgrading](https://fernandezvara.github.io/backd/docs/operate/); the notes of every release are on the [releases page](https://github.com/fernandezvara/backd/releases).

Until a stable release is declared, a minor release may have breaking changes; they are listed first.

## Unreleased

### Security

- **Breach response review:** a second step, an enrollment or a consent prompt that a password sign-in started before the passwords were expired can no longer finish into a session; using a reset link uses up the user's other reset links; an anonymized export no longer carries the production history of passwords (`password_expired_at`, `password_notified_at`) and gives the staging password a current change time.
- **Export and import review:** the manifest's authentication parts and every collection part must be the path the format names and be checksummed (a hostile package could make `backd import` read a local file); an imported document's `_meta` is held to the fields the server writes; imported files are held to their field's size; `--auth-key-out` creates a `0600` file even over an existing one; anonymized users lose `admin_networks` and `login_networks`. See the security model page.

### Added

- **Search on encrypted fields (third part of field encryption):** `fields.<path>.encrypt` takes `search: equality`, `normalize: [trim, lower, upper]` and `unique: true`. `backd` keeps a hidden, indexed HMAC-SHA-256 of the normalized value under a random index key (wrapped by the field key, rotated with it); `where` with `=`, `$eq` or `$in` on the field is answered through it, `unique` makes two equal values a `409`, and a read rule may compare the field with `==` or `in` against a value that doesn't come from the document (pushed into the query; `!=`, ranges and the rest are refused at startup). Anything else on an encrypted field is still `400 field_encrypted`. Equal values share an index, which the docs warn about for low-variety data. Existing documents have no index until the migration of a later release. A `_bidx` field is reserved. `backd provision` creates the indexes.
- **Encrypted fields (second part of field encryption):** `collection.yaml`'s `fields.<path>.encrypt.key` stores a field encrypted with AES-256-GCM before it reaches MongoDB: any declared field or nested path, any JSON type, bound to its realm, database, collection, path and document id, under a data key per field that the field key wraps. Everything reads the plain value (API, `_batch`, functions, admin data, triggers, realtime); `schema.json` validates the plain value and the database validator expects binary. `backd secret rotate --name HEALTH --generate|--value|--resume` (`POST /_admin/secrets/{name}/rotate`) replaces a key and wraps its data keys again without rewriting a document; the previous value is kept until it is done, and a key with data keys under it can't be deleted. Startup refuses encrypted fields that an index, a reference, a file field, a date, an array, an erase `replace`/`pull`/`unset`, an aggregation or a read rule needs in plain; `where` and `order_by` on an encrypted field answer `400 field_encrypted`. New system collection `data_keys`: run `backd provision`. Collections with encrypted fields can't be exported or imported yet. `backd-js` gains `admin.secrets.rotateKey`.
- **Field keys (first part of field encryption):** `backd secret create --realm r --type key --generate --name HEALTH` (or `--value key:|env:|file:` to import one) creates a 32-byte key that is shown once, with a fingerprint to check a saved copy against; `POST /_admin/secrets` does it through the API, and the listing shows each key's fingerprint and the fields that use it, never its value. Needs full admin access and a fresh proof; audited as `secret.key.create` and `secret.key.delete`; a key a field names can't be deleted, a plain `PUT` can't replace one, and no function or provider can read one. `collection.yaml` accepts `fields.<path>.encrypt.key`, and startup refuses a field whose key is missing or is a plain secret. Encrypting the data follows. `backd-js` gains `admin.secrets.createKey`. A key that is lost is lost for good: save it before using it.
- **Documentation:** [If credentials leak](https://fernandezvara.github.io/backd/docs/operate/if-credentials-leak/), a runbook for a leaked backup, dump or key: the steps in order with their commands, what users will see and measured timings. The backup guide's leak section points to it.
- **Password expiry (first part of breach response):** a password can be expired, so it no longer opens the account until the user resets it. Password identities record `password_changed_at`, the realm has a password epoch (`security_state`), and `backd user expire-password` / `POST /_admin/users/{id}/expire-password` expire one user's. A correct expired password gets `401 password_expired` (a wrong one is still `invalid_credentials`) and queues a reset email; password re-authentication refuses it too. Run `backd provision` to update the system collections' validators. `backd user expire-passwords --realm r --yes` (`POST /_admin/passwords/expire`) moves the realm's epoch to now and ends every session (`--keep-sessions` to keep them); `backd user revoke-sessions --realm r --all --yes` (`DELETE /_admin/sessions`) ends every session alone. Both need full admin access and a fresh proof of identity, and are audited. `--notify` (and `backd user notify-expired`, `POST /_admin/passwords/notify`) starts a resumable job that emails every affected user the new `password-expired` message with a reset link, once each and within the email limits; a refused sign-in sends the same message. `backd user reset-link` (`POST /_admin/users/{id}/reset-link`) prints a one-time reset link for a user the email can't reach, sending nothing, audited as `password.reset_link`. `backd-js` raises a typed `PasswordExpiredError` (`resetEmailQueued` says whether backd queued the email with the link) and its admin client gains `users.expirePassword`, `users.resetLink`, `passwords.state/expireAll/notify` and `sessions.revokeAll`. The admin interface's Users page gets a *Passwords and sessions* panel for a full administrator (expire every password, tell the users, end every session), and a user's page can expire the password and make a reset link. `GET /_admin/passwords` reports the realm's epoch and how many accounts have a password; `GET /_admin/users/{id}` says whether the password is expired now.

- **Documentation:** the guides and reference of [exporting and importing](https://fernandezvara.github.io/backd/docs/operate/administration/export-import/): moving an app, seeding staging, reporting extracts, direct mode, the package format and manifest, every flag, the admin API, the errors with what to do, and keeping packages and keys safe.
- **Export packages:** `backd export --realm r [--db d] [--collection c,…] --out dir [--archive] [--no-files]` writes a realm's documents (soft-deleted ones included, in the API representation with Extended JSON for dates and binary) and files as a lossless package with a checksummed manifest, through `GET /_admin/export` (full admin access, re-authentication for sessions, one export per realm at a time, resumable). Audited as `export` and `export.done`. Import, authentication data, direct mode and anonymized exports follow.
- **Import:** `backd import --from <package|.tar.gz> [--mode create|skip|replace] [--dry-run] [--skip-invalid] [--report file]` verifies the package's checksums, has the running `backd` validate every document against the target realm's configuration (ids, schemas, file limits, unique values, existing documents) before anything is written, uploads the files and writes the documents with their ids and `_meta` kept. Writing is idempotent, so an interrupted import is finished by running it again. Through `POST/PUT/DELETE /_admin/import`; audited as `import` and `import.done`.
- **Anonymized exports for staging:** `backd export --anonymize [--anonymize-config file] [--staging-password env:…] [--anonymize-files keep]` applies each collection's `on_owner_delete` `remove` and `replace` rules to every document (plus per-collection overrides, checked against the schemas), rewrites the emails users are listed under in `pull`/`unset` fields, leaves files out, and with `--include auth` gives users `user-<id>@example.invalid` addresses, drops password hashes (or sets one known password), second factors, devices and external-provider identities. The manifest is marked `anonymized`. New `BACKD_ENV` (`production`, `staging`…): an import warns about a non-anonymized package on `staging` and an anonymized one on `production`. `POST /_admin/export` carries the options that don't belong in a URL.
- **Direct mode:** `backd export --direct` and `backd import --direct` (with `--mongo-uri`, `--master-key`, `--storage-key`/`--storage-secret`, `--force`) run the same export and import in the command, straight on MongoDB and `CONFIG_DIR`, with no server: the master key is needed only for files and the realm-sealed authentication data. Records are audited as `cli:offline` with `offline: true`. A direct import refuses while a `backd serve` instance of the realm is alive (instances now record a heartbeat in `backd___deployment.instances`) unless `--force`. `DELETE /_admin/export` now answers `200` with the export's totals.
- **Authentication data in packages:** `backd export --include auth [--auth-key-out file]` adds the realm's users, identities with password hashes, organizations, memberships, teams, authenticator enrollments and push devices; the fields sealed with the realm's secrets are re-sealed under a one-time export key shown once, and `backd import --auth-key key:|env:|file:` opens them on the operator's machine for the target to seal under its own key. Without the key, users arrive without second factors and devices. Recovery codes can't travel. Audited as `export.auth`.
- **Reporting extracts and CSV:** `backd export --fields a,b.c --where '{…}' --format csv` narrows an export to some fields and the documents matching a filter (for one collection), and writes one RFC 4180 table per collection with flattened columns. Field-selected packages are marked `partial`; CSV is lossy and never importable.

## 0.9.0

No breaking changes and nothing to provision: the server only gains `backd types --lang dart` and `?lang=dart` on the admin types endpoint. If you run `PROVISION_MODE=verify`, no `backd provision` run is needed for this release alone.

### Added

- **Dart client:** `package:backd` on pub.dev (`dart pub add backd`), versioned on its own (`dart-v*` tags). Sign-in with the second-factor, magic-link, provider and consent steps, collections and the query language, files with direct uploads and image versions, function jobs, live lists over realtime, push device registration, app configuration, organizations and generated realm types (`BackdClient<ShelfRealm>`). It has **no admin API, by design**: administration stays in the JavaScript client, the admin UI and the CLI. The Flutter Shelf demo (`clients/dart/example`) is the tutorial's app end to end.
- **Dart types:** `backd types --lang dart` writes a realm's `types.dart` (a `ShelfRealm` accessor and document, input, `where`/`orderBy`, function and configuration classes) next to the TypeScript output, and the admin types endpoint takes `?lang=dart`. `backd openapi` keeps feeding openapi-generator for Kotlin, Swift and the rest; `make openapi-clients` builds all three from the example realms' documents.
- **Project template:** `backd template project` scaffolds a ready-to-use config repository — a realm with a sample database (a `posts` collection and a `stats` function with tests), a local development stack on the released images, a check-only GitHub workflow and an `AGENTS.md` for AI coding assistants.
- **JavaScript client 0.9.0:** released on the same version as the server, unchanged from 0.8.5.
- **Documentation:** reorganized per reader (build, operate, contribute), the [Dart client guide](https://fernandezvara.github.io/backd/docs/build/clients/dart/) and the [development environment](https://fernandezvara.github.io/backd/docs/contribute/) updates.

## 0.8.5

No breaking changes for existing configuration: realtime and triggers are off until a collection declares them. Declaring either needs `backd provision` in `PROVISION_MODE=verify` (pre-change images, new `jobs` indexes), and realtime needs **MongoDB 6.0 or later as a replica set** and a `MONGO_URI` user with the `changeStream` and `find` actions on every database (for example `readAnyDatabase`); `backd` checks all of it at startup and names what's missing. Writes a trigger run makes are stamped `_meta.chain`, a field a client can't set and a function can't drop.

### Added

- **Realtime subscriptions:** `realtime: true` in `collection.yaml` and `POST /v1/{realm}/_realtime`, one server-sent event stream per caller following up to 20 subscriptions (`where` filters or single documents). Events are live-query semantics — `created`, `updated`, `removed` mean entered/changed/left *this caller's* view, so the read rule is evaluated per change and a stranger learns nothing — with signed resume tokens (`Last-Event-ID`), `reset` events that say why to reload (`history_expired`, `slow_consumer`, `preimage_missing`, `access_changed`, `config_changed`), `session_ended` and `reauth` closes, per-instance limits (`BACKD_REALTIME_*`, lowerable per realm), a memory budget that resets the slowest streams instead of stalling, and `BACKD_EVENTS=false` to keep stream work off request-serving replicas. Needs MongoDB's change stream: pre-change images per collection and a user that may read the deployment's stream, both checked at startup.
- **Triggers:** `triggers:` in `collection.yaml` run an internal async function when documents are created, updated or removed (`events`, a `when` condition on `document`/`before`/`changed()`/`appConfig()`, `fields` to carry paths, `session: true` for the writer's name, `ordered: true` for one document's runs in order). One leased dispatcher per deployment (`BACKD_TRIGGERS`) queues deduplicated, at-least-once jobs; the function always has `ctx.admin.db`. Chains are bounded (`triggers.max_chain`, `max_chained_per_minute`) and a realm's backlog is capped (`max_backlog`), what can't be queued is recorded as a **gap** — inspectable with `backd triggers status|gaps|resolve` over the admin API and repaired with `backd triggers rescan`. Metrics `trigger_dispatch_lag_seconds`, `trigger_gaps_total`, `trigger_dropped_total`, `trigger_backlog`, `jobs_serialized_waiting`.
- **Exact built-in hooks:** `account.on_signup` and the `organizations.hooks.*` jobs are now queued in the same MongoDB transaction as the change that caused them, with an id made of the event, so a retry is one job, not two.
- **JavaScript client 0.8.5:** `client.realtime.subscribe()` and `collection.subscribe()` return a `LiveView`: a list that loads first, then keeps itself current through resets (with jittered reloads), reconnects with resume tokens, and reports `connecting`/`live`/`resyncing`/`closed` per subscription. Typed errors `RealtimeNotEnabledError`, `TooManySubscriptionsError`, `InvalidResumeTokenError`, `SessionEndedError`, and the generated trigger input types.
- **Development stack:** `make dev` (`docker-compose.dev.yml`) serves released images over your own `./config` with live reload, the API and worker apart (`WORKERS=`), Prometheus and Grafana; `make dev-examples` runs the sample realms on it. Plus `make perf`: load and soak suites for the trigger dispatcher and the realtime hub with measured numbers in the docs.
- **Tutorial:** chapter 23 (a status pipeline built with triggers) and chapter 24 (a team pinboard kept live).
- **Documentation:** [Realtime](https://fernandezvara.github.io/backd/docs/build/api/realtime/) (events, resuming, limits, access changes), [Triggers](https://fernandezvara.github.io/backd/docs/build/functions/triggers/) (input, at-least-once handlers, chains, ordering, debugging), [Change feed](https://fernandezvara.github.io/backd/docs/operate/change-feed/) (requirements, cost, the dispatcher, sizing the oplog) and the [development stack](https://fernandezvara.github.io/backd/docs/contribute/).

### Not yet

- `_batch` read operations (it only writes), `$search` and `$like` inside a subscription's `where`, and subscribing to backd's own collections wait for later releases.

## 0.8.4

No breaking changes, apart from two names reserved in `schema.json` (`_expanded`, `_search_lang`). No new system collections; declaring a text index or the indexes an aggregation asks for needs `backd provision` in `PROVISION_MODE=verify`.

### Added

- **References and expansion:** `references:` in `collection.yaml` (checked at startup: target, source field, unique key), `?expand=author(name),tags,category.parent` on a get or a list (one batched query per path and level, the target's own read rule with the caller's identity, API key scopes, identical `null`s for missing, unreadable and foreign documents, limits `BACKD_EXPAND_MAX_*`, cost headers `X-Backd-Expand-*`, never cached publicly), in functions (`ctx.db` as the caller, `ctx.admin.db` as admin) and in the JavaScript client and the testing fake.
- **Aggregations:** `aggregate:` in `collection.yaml` and `GET …/_aggregate`: group by up to three declared fields with date buckets (day, ISO week, month, year) in an IANA time zone; count, count_distinct, sum, avg, min and max with exact decimals; the read rules applied before grouping; a fixed pipeline with a time limit, no disk use, a group cap and one request at a time per caller and collection; weak `ETag`; metrics `backd_aggregations_total` and `backd_aggregation_duration_seconds`.
- **Full-text search:** a text index in `indexes.json` (weights, `default_language`, `language_field`, rebuilt when its declaration changes), `$search` in `where` (words, phrases, exclusions; at most 256 bytes and 10 terms), `language=`, `order_by=_relevance` with `_meta.score`, `$search` inside `_aggregate`, `422 search_too_expensive` and `503 index_building`.
- **JavaScript client 0.8.4:** `expand()`, `aggregate()`, `search()`, `fillBuckets()`, `_expanded` and aggregations typed from the generated types, and typed errors (`InvalidExpandError`, `ExpandTooLargeError`, `AggregateNotEnabledError`, `InvalidAggregateError`, `AggregateTooExpensiveError`, `SearchNotEnabledError`, `SearchTooExpensiveError`, `IndexBuildingError`).
- **Tutorial:** chapter 21 (authors through a reference) and chapter 22 (search and a team dashboard).
- **Documentation:** [References and expansion](https://fernandezvara.github.io/backd/docs/build/api/references/), [Aggregations](https://fernandezvara.github.io/backd/docs/build/api/aggregations/) with best practices, the [search guide](https://fernandezvara.github.io/backd/docs/build/api/search/), and the security review of each in the security model.

### Not yet

- `_batch` read operations (it only writes), realtime refusing `$search`, and refusing encrypted fields in aggregations and text indexes wait for realtime and field encryption.

## 0.8.3

No breaking changes. New system collections: `PROVISION_MODE=verify` instances need `backd provision` first.

### Added

- **Organizations:** groups of users per realm, with roles, memberships, invitations (by user, address or open link), teams, a second factor required per organization, and deletion with a report and a resumable cleanup. Documents belong to an organization (`_meta.org`) and rules ask about it with helpers that are pushed down to the query; a realm administrator can move documents between organizations.
- **Team access:** team roles with time windows and delegation, a second factor required per role, documents that belong to a team (`_meta.team`, `?team=`, `on_team_delete`), and membership hooks (`on_member_joined`, `on_member_left`, `on_roles_changed`) with a sequence per organization and user.
- **Organization context:** the optional `org` on sign-up, login, magic links and reset, `email.data_hook` and `pages.data_hook`, and organization versions of the realm's emails (logic-less templates, sanitized HTML, preview and test send, versions).
- **Surfaces:** functions (`ctx.orgs`, team roles), the admin API and UI (organizations, members, teams, team roles), the `backd data transfer --team` command, and `backd rules test` fixtures with `orgs:`.
- **Documentation:** the Organizations section (concepts, a B2B app recipe, rules cookbook, team roles and recipes, an HR absences example, hooks, organization context, email templates).
- **JavaScript client 0.8.3:** organizations, teams, team roles, invitations, organization email templates and `OrgRoleRequiresMFAError`.

### Changed

- New dependency `golang.org/x/net` (HTML tokenizer for the template sanitizer); the admin UI and the JavaScript client's tooling dependencies are updated (TypeScript 7, `@types/node` 26).

Not yet: refreshing open realtime streams at team-role window boundaries and `_meta.team` in aggregations wait for realtime and the aggregate endpoint.

## 0.8.2

### Breaking

- Lists without `order_by` are ordered by `_meta.created_at`, then `id`. A cursor is opaque; a hand-made `after=<id>` needs `order_by=id`. Every data collection gets a `_meta.created_at,id` index (run `backd provision`).
- A create that sends `id` to a collection without `custom_id` is `400 id_not_allowed` (it was ignored).
- Sensitive account actions ask for a fresh proof (`POST /_auth/reauth`, header `X-Backd-Reauth`); their `password` and `current_password` fields are gone.
- `email.default_locale` and `email.locales` moved to the top level of `realm.yaml` (`default_locale`, `locales`).
- The return parameters of provider sign-in are prefixed `backd_`.
- New system collections: `PROVISION_MODE=verify` instances need `backd provision` first.

### Added

- **Sign-in:** Google, Microsoft and Apple through a redirect or a native ID token, several sign-in methods per user, accounts without a password, magic links and codes by email, a second factor (authenticator app and recovery codes) that a realm can require of roles, `user.mfa` and `user.methods` in rules and functions, a fresh proof for sensitive actions, and the matching commands and admin pages.
- **Push notifications:** device registry, providers (FCM, APNs, browsers), templates, `ctx.push.send`, send records and metrics, development capture and a Push tab in the Mailbox.
- **Compliance:** personal data scope and report, consent documents and records, consents at sign-up and re-acceptance, withdrawal, gating of email and push, and retention at erasure.
- **Caching:** conditional GET for documents and lists, public caching of anonymous reads, and a cache in the JavaScript client.
- **App configuration:** a schema and read rules per realm, versions, history and rollback, conditional reads (`GET /_config`), `appConfig()` in rules and `ctx.config` in functions, and `client.config`.
- **Types and OpenAPI:** `backd types` (TypeScript for the app and for functions, `defineFunction`), `backd openapi` and `GET /_openapi.json` (a document per realm, and an admin one), stale-types detection, and downloads in the admin UI.
- **Development:** `BACKD_DEV=true` reloads the whole config live and rewrites the generated types.
- **Custom ids:** `custom_id` in `collection.yaml` (a pattern, or `user`), creates with `id`, `409 id_taken` with a limit on duplicates, and `backd_id_conflicts_total`.
- **Tutorial:** chapters 16 to 19 (second factor, consents, app configuration, types), and Shelf's `members` keyed by user id.
- **JavaScript client 0.8.2:** the second step and magic links, provider sign-in, push, consents, `client.config`, caching, typed clients, `InvalidIdError` and `IdTakenError`, and the type-level tests are now part of the repository.

### Changed

- Go 1.27.2 and kli 0.3.0; the docs theme is hugodoks 0.3.0.

## 0.8.1

Image versions, metadata removal from JPEG and PNG, storage quotas, multipart direct uploads up to 4 TiB, `rules.yaml` merged into `collection.yaml`, and `backd` without a command printing its help. See [upgrading to v0.8.1](https://fernandezvara.github.io/backd/docs/operate/#upgrading-to-v081).

## 0.8.0 and earlier

Files in S3-compatible storage, schema checks, `on_complete`, job steps, the admin web interface, soft delete, functions and email. See the release notes and the upgrade notes of each version.
