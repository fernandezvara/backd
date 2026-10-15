# Changelog

All notable changes to `backd` and to its JavaScript client `backd-js`, which are released on the same version from 0.8.2. The upgrade notes, with what needs your action, are in the documentation under [Operations → Upgrading](https://fernandezvara.github.io/backd/docs/operations/); the notes of every release are on the [releases page](https://github.com/fernandezvara/backd/releases).

Until a stable release is declared, a minor release may have breaking changes; they are listed first.

## 0.8.4

No breaking changes, apart from two names reserved in `schema.json` (`_expanded`, `_search_lang`). No new system collections; declaring a text index or the indexes an aggregation asks for needs `backd provision` in `PROVISION_MODE=verify`.

### Added

- **References and expansion:** `references:` in `collection.yaml` (checked at startup: target, source field, unique key), `?expand=author(name),tags,category.parent` on a get or a list (one batched query per path and level, the target's own read rule with the caller's identity, API key scopes, identical `null`s for missing, unreadable and foreign documents, limits `BACKD_EXPAND_MAX_*`, cost headers `X-Backd-Expand-*`, never cached publicly), in functions (`ctx.db` as the caller, `ctx.admin.db` as admin) and in the JavaScript client and the testing fake.
- **Aggregations:** `aggregate:` in `collection.yaml` and `GET …/_aggregate`: group by up to three declared fields with date buckets (day, ISO week, month, year) in an IANA time zone; count, count_distinct, sum, avg, min and max with exact decimals; the read rules applied before grouping; a fixed pipeline with a time limit, no disk use, a group cap and one request at a time per caller and collection; weak `ETag`; metrics `backd_aggregations_total` and `backd_aggregation_duration_seconds`.
- **Full-text search:** a text index in `indexes.json` (weights, `default_language`, `language_field`, rebuilt when its declaration changes), `$search` in `where` (words, phrases, exclusions; at most 256 bytes and 10 terms), `language=`, `order_by=_relevance` with `_meta.score`, `$search` inside `_aggregate`, `422 search_too_expensive` and `503 index_building`.
- **JavaScript client 0.8.4:** `expand()`, `aggregate()`, `search()`, `fillBuckets()`, `_expanded` and aggregations typed from the generated types, and typed errors (`InvalidExpandError`, `ExpandTooLargeError`, `AggregateNotEnabledError`, `InvalidAggregateError`, `AggregateTooExpensiveError`, `SearchNotEnabledError`, `SearchTooExpensiveError`, `IndexBuildingError`).
- **Tutorial:** chapter 21 (authors through a reference) and chapter 22 (search and a team dashboard).
- **Documentation:** [References and expansion](https://fernandezvara.github.io/backd/docs/api/references/), [Aggregations](https://fernandezvara.github.io/backd/docs/api/aggregations/) with best practices, the [search guide](https://fernandezvara.github.io/backd/docs/api/search/), and the security review of each in the security model.

### Not yet

- `_batch` read operations (it only writes), realtime refusing `$search`, and refusing encrypted fields in aggregations and text indexes wait for realtime and field encryption.

## 0.8.3

No breaking changes. New system collections: `PROVISION_MODE=verify` instances need `backd provision` first.

### Added

- **Organizations:** groups of users per realm, with roles, memberships, invitations (by user, address or open link), teams, a second factor required per organization, and deletion with a report and a resumable cleanup. Documents belong to an organization (`_meta.org`) and rules ask about it with helpers that are pushed down to the query; a realm administrator can move documents between organizations.
- **Team access:** team roles with time windows and delegation, a second factor required per role, documents that belong to a team (`_meta.team`, `?team=`, `on_team_delete`), and membership hooks (`on_member_joined`, `on_member_left`, `on_roles_changed`) with a sequence per organization and user.
- **Company context:** the optional `org` on sign-up, login, magic links and reset, `email.data_hook` and `pages.data_hook`, and company versions of the realm's emails (logic-less templates, sanitized HTML, preview and test send, versions).
- **Surfaces:** functions (`ctx.orgs`, team roles), the admin API and UI (organizations, members, teams, team roles), the `backd data transfer --team` command, and `backd rules test` fixtures with `orgs:`.
- **Documentation:** the Organizations section (concepts, a B2B app recipe, rules cookbook, team roles and recipes, an HR absences example, hooks, company context, email templates).
- **JavaScript client 0.8.3:** organizations, teams, team roles, invitations, company email templates and `OrgRoleRequiresMFAError`.

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

Image versions, metadata removal from JPEG and PNG, storage quotas, multipart direct uploads up to 4 TiB, `rules.yaml` merged into `collection.yaml`, and `backd` without a command printing its help. See [upgrading to v0.8.1](https://fernandezvara.github.io/backd/docs/operations/#upgrading-to-v081).

## 0.8.0 and earlier

Files in S3-compatible storage, schema checks, `on_complete`, job steps, the admin web interface, soft delete, functions and email. See the release notes and the upgrade notes of each version.
