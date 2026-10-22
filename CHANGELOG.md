# Changelog

All notable changes to `backd` and to its JavaScript client `backd-js`, which are released on the same version from 0.8.2. The upgrade notes, with what needs your action, are in the documentation under [Operations → Upgrading](https://fernandezvara.github.io/backd/docs/operate/); the notes of every release are on the [releases page](https://github.com/fernandezvara/backd/releases).

Until a stable release is declared, a minor release may have breaking changes; they are listed first.

## Unreleased

### Added

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
