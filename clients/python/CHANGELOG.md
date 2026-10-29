# Changelog

## 0.9.6

From this version the package is released on the same version as the `backd` server (before, `0.0.x`).

- First version: `Client` and `AsyncClient` with the request pipeline, errors, retries, token storage and the size helpers.
- `client.auth`: sign-up, login and the sign-in steps, MFA, magic links, providers (redirect and ID token), `reauth`/`with_proof`, sessions, the email flows, `client.org` and `on_auth_change`.
- Collections: `list`, `iterate`, `search`, `aggregate`, `get`, `create`, `replace`, `patch`, `delete`, `restore`, `db.batch`, `in_team`/`in_org`, the query-language helpers, `expand` and `fill_buckets`.
- The revalidation cache (`If-None-Match`/`304`, LRU by entries and bytes, per credential; `cache_stats`, `clear_cache`, `cache=False` per read) and `client.config` (`load`, `get`, `get_sync`, `view`, `on`, persisted copies through `config_storage`).
- Files: `files(id, field)` (`list`, `get`, `put`, `delete`, `link`, `version`), `upload_file` (through backd, direct to the bucket, in parts), `prepare_upload`, `download_file` (streams to a path or file, checks the SHA-256), `file_url`, `public_url`, `version_status`, `Upload` and progress callbacks.
- Functions and jobs: `db.fn`, `collection.fn`, `client.functions.call` / `run`, `Job` with `status`, `wait` (polling, timeout, progress) and `steps`, and `JobTimeoutError`.
- `client.orgs` (organizations, members, teams and team-role windows, email templates, invitations, sign-in connections), `client.consents`, `client.push` (`register`, `devices`, `unregister`, `register_on_launch`) and `client.account` (data export: request, wait, download).
- Realtime: `client.realtime.subscribe` and `collection.subscribe` over one multiplexed SSE stream, `LiveView` (threads) and `AsyncLiveView` (tasks) with `LiveState`, resume tokens, reset with jitter, reconnect with backoff, references, `access_lost`, and `client.config.live` (the configuration follows the stream). Closing the client leaves no thread or task running.
- Generated types: `backd types --lang python` writes `types.py` (`TypedDict`s, `Literal`s, field constants, `Realm` and `AsyncRealm` wrappers); the Shelf example's types are checked with `mypy --strict` and for drift.
- Release tooling: `scripts/check-python-package.sh` (`make python-package`) builds the sdist and the wheel, checks their metadata and contents and uses the wheel from a fresh virtualenv; `publish-python.yml` publishes to PyPI on `py-v*` tags with trusted publishing.
