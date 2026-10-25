## Unreleased

- **Breaking, with a backd server that scopes sessions (34.2):** `Collection.inOrg()` and the `org` option of `create`, and `TypedCollection.inOrg`, are gone: a session works in the one organization it was opened for, so there is none to name (`inTeam` needs no `inOrg` before it). `orgs.list()`, `orgs.previewInvitation` and `orgs.acceptInvitation` are gone too: use `orgs.signedIn()`, `auth.previewInvitation(token, org:)` and a login with the `invitation:`. A `session_unscoped` answer expires the session like `unauthenticated`. `orgs.update` takes `sessionMaxAge`.
- **Signing in to an organization:** `BackdClient(org: 'acme-corp')` binds the client to an organization (an id, or the slug people know) and `login`, `signup`, `magicLink.request`, `requestPasswordReset` and `signInWith` go to `/_auth/orgs/{org}/…`, so the session is scoped to it (`Session.org`, `auth.current`, `Me.org`, `auth.org()`); a wrong password, a stranger and an unknown organization fail alike. An invitation is used as part of the sign-in (`invitation:`), `auth.previewInvitation` reads it before, `auth.options()` lists what the sign-in page can offer, `auth.signUpWithOrg` starts a company and `auth.findOrgs` emails an address its companies. `client.org(id)` gives one client per organization with a token of its own, and `client.orgs.signedIn()` lists them. **`TokenStorage` gains `slot(name)` and `slots()`** (breaking for custom storages: return null from `slot` to keep one token). `orgs.create` keeps the session of the organization it makes. Needs a backd server with the organization sign-in routes.

## 0.9.2

- **Your own data:** `client.account` asks for the export of the user's
  personal data (`requestExport`, with a fresh proof), follows it (`exports`,
  `export`, `waitForExport`) and saves it (`downloadExport`: a signed link
  and its `bytes()`, or the zip itself when the realm proxies its files). Needs
  a backd server with `compliance.export`. See
  [Personal data export](https://fernandezvara.github.io/backd/docs/build/auth/personal-data/).

- **Live app configuration:** `client.config.live([paths])` follows the realm's
  app configuration over realtime. `config.changes` fires within the realtime
  delay instead of at the next `cache_ttl`; `config.errors` reports a realm
  without configuration or without realtime. Needs a backd server with the
  `config` realtime subscription. See
  [Live updates](https://fernandezvara.github.io/backd/docs/build/configuration/app-config/#live-updates).

## 0.9.1

- **Versions now follow the `backd` release train.** `package:backd` 0.9.1
  goes with `backd` v0.9.1 and `backd-js` 0.9.1, as `backd-js` has done since
  0.8.2. The API is the same as in 0.0.5; the number is the only change.
- **Update the constraint.** A caret on a `0.0.x` version allows nothing
  newer, so `backd: ^0.0.5` will not pick this release: use `backd: ^0.9.1`
  (`dart pub add backd`). From here a `backd` X.Y matches the server's X.Y,
  and patch releases are fixes.
- The Flutter Shelf demo moved out of the package, from `example/` to
  `clients/dart-examples` in the repository. The package no longer needs the
  Flutter SDK to resolve, test or publish.
- Works with everything `backd` v0.9.1 adds on the server side:
  [encrypted fields](https://fernandezvara.github.io/backd/docs/build/encryption/)
  read as `null` with `_meta.redacted` for a caller their reveal rule refuses
  (already typed since 0.0.5), and `PasswordExpiredError` and
  `FieldEncryptedError` for the new server errors. The Dart client has no
  admin API, so the new `admin.encryption`, key and breach-response commands
  stay in `backd-js`, the CLI and the admin UI.

## 0.0.5

- New errors: `PasswordExpiredError` (`password_expired`, with
  `resetEmailQueued`) and `FieldEncryptedError` (`field_encrypted`), matching
  `backd-js`.
- `fillBuckets` and `bucketKey` for aggregations over date buckets.
- `Collection.deleteFile` and `Collection.downloadFile`, which the
  documentation already described.
- Generated types: `Meta.redacted`, the encrypted fields a caller may not see.
- The example app builds with `file_picker` 11 (`FilePicker.pickFiles` is
  static), which fixes `dart analyze --fatal-warnings` for the package.

## 0.0.4

- Upgraded library and dependency versions across the package and the
  `example/` app.
- CHANGELOG now covers every published release.## 0.0.3

- Feature modules landed on top of the `0.0.1` scaffold: `auth` (sessions,
  OAuth, consents), `data` (collections and the query language), `files`,
  `functions` (jobs), `realtime` (live views), `orgs`, `push`, typed realm
  clients and token `storage`.

## 0.0.2

- Publication tests: pub.dev packaging dry-runs, `.pubignore` fixes and CI
  release plumbing. No API changes.

## 0.0.1

- First development release: the package scaffold — `BackdClient` options, the
  injectable transport, the request pipeline (JSON and raw bodies, query
  encoding, retries of `429`/`503` on safe requests, the `X-Backd-Types`
  fingerprint headers), the `BackdException` hierarchy, `TokenStorage` and the
  `RevalidationCache`. Feature modules (auth, data, files, realtime, …) land
  in following releases.
