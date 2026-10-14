# Changelog

All notable changes to `backd` and to its JavaScript client `backd-js`, which are released on the same version from 0.8.2. The upgrade notes, with what needs your action, are in the documentation under [Operations → Upgrading](https://fernandezvara.github.io/backd/docs/operations/); the notes of every release are on the [releases page](https://github.com/fernandezvara/backd/releases).

Until a stable release is declared, a minor release may have breaking changes; they are listed first.

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
