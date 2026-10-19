# AGENTS.md — working on this repository

This is a [backd](https://github.com/fernandezvara/backd) config repository, created by `backd template project`. `config/` is `CONFIG_DIR`: the whole backend lives there — the application talks to the running backd's HTTP API, never to MongoDB directly.

## Concepts

- **Project** — this repository: `compose.yaml` (the local dev stack), `config/`, `.github/workflows/backd.yml` (check-only). Generated once; everything here is yours to change.
- **Realm** — `config/<realm>/` with a `realm.yaml`. A realm is a tenant: it owns its users, roles, email and push templates, storage and app_config. This project has one realm, `__REALM__`; a config can hold several.
- **Database** — `config/<realm>/<db>/`. Each database maps to one MongoDB database named `<realm>__<db>` (`__REALM__` ships `main` → `__REALM____main`). Its directory holds collections and one `_functions/` project.
- **Collection** — `config/<realm>/<db>/<collection>/` with a required `schema.json` (JSON Schema for its documents) and `collection.yaml` (`rules:` who may read/write, `soft_delete`, `erasure`, `indexes` live in `indexes.json`). `rules.test.yaml` (optional) defines access-rule cases that `backd rules test` runs without a database.
- **Function** — `config/<realm>/<db>/_functions/<name>/` with `function.yaml` (permissions, schedule, dev_only) and `index.ts` (a `defineFunction` handler with `ctx`: `ctx.db`, `ctx.user`, `ctx.call`, `ctx.email`, `ctx.push`, `ctx.files`, `ctx.http`). Deno TypeScript, bundled by `backd functions build` into `.build/` — generated, gitignored, never edited. `lib/` is shared code; `deno.json` pins its imports.

## Working rules

- Secrets never go into config files — reference them as `secret:NAME` and set them with `backd secret set` against the running instance.
- Every collection needs `schema.json` and `collection.yaml`; validate with `CONFIG_DIR=./config backd config check` and `backd rules test`.
- After editing function code, the bundle must be rebuilt: locally `docker compose up` already rebuilds on save (BACKD_DEV); in CI or production run `backd functions build`.
- Scaffold more pieces with the CLI: `backd template collection-policy`, `template function`, `template database`, `template realm`, `template email-capture`.
- Don't hand-edit generated files: `_functions/.build/`, `_functions/.backd/`, `.backd-importmap.json` (all gitignored), and `backd.d.ts` (regenerated, committed).

## Clients

Applications use the API clients, not the config files:

- **JavaScript/TypeScript** — [`backd-js`](https://www.npmjs.com/package/backd-js) on npm: `createClient(url, realm, db)` then `insert`/`find`/`get`/`update`/`remove`/`call`/`files`.
- **Dart/Flutter** — [`backd`](https://pub.dev/packages/backd) on pub.dev.
- **Anything else** — the REST API directly: `POST/GET /v1/<realm>/<db>/<collection>[/<doc>]`, `/v1/<realm>/<db>/_functions/<name>`, auth under `/v1/<realm>/_auth/`, admin under `/v1/<realm>/_admin/` (see `api/openapi.yaml` in the backd repo).
- **Function tests** — `deno test config/<realm>/<db>/_functions/` with `_functions/lib/testing.js` (a fake `ctx` with an in-memory `ctx.db`); no MongoDB or running backd needed.

## Local environment

`docker compose up` (create `.env` with `EXECUTOR_TOKEN`, `CALLBACK_KEY`, `EGRESS_KEY` first — see README.md). API on `http://127.0.0.1:8080`, MinIO console on `http://127.0.0.1:9001`. Dev mode reloads config/ live: edit and save, no restarts.

Deeper reference: <https://fernandezvara.github.io/backd/docs/>
