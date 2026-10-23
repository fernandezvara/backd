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
