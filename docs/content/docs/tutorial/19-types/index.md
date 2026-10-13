---
title: "19. Types from the config"
description: "Generate TypeScript types from Shelf's config for the app and the functions, watch them follow a schema change in dev mode, see the stale-types warning, and keep them current in CI."
weight: 390
toc: true
---

Shelf's config already says what an asset is, what `publish` takes and what the configuration holds. This chapter lets editors and compilers know it too: `backd types` turns the config into **TypeScript types**, for the app (`createClient<Realm>()`) and for the functions (`defineFunction`). Nothing changes at run time: the server still validates every request; the types only catch mistakes before you run anything.

## Types for the app

```sh
CONFIG_DIR=./config backd types --realm shelf --out clients/js/examples/shelf/backd
# wrote …/backd/types.d.ts and …/backd/realm.json (types 1d77f90bf5e15660)
```

`types.d.ts` holds one `Realm` interface: every collection's document and `where`, the functions' input and result, the configuration's paths, the templates and the roles. The app gives it to the client as a type:

```js
/** @typedef {import('./backd/types').Realm} Realm */

/** @type {import('backd-js').TypedClient<Realm>} */
const backd = createClient({ url: window.location.origin, realm: 'shelf', types: devTypes })

backd.db('main').collection('assets').list({ where: { kind: 'link' }, orderBy: '-_meta.created_at' })
backd.db('main').fn('publish', { asset_id })        // typed input
backd.config.get('maintenance.enabled')             // boolean | undefined
```

A misspelled collection, a field the schema doesn't have, an operator a type doesn't allow and a wrong function input are now red in the editor ([the client reference](../../clients/js/#typed-client) lists what is typed).

## Types for the functions

```sh
CONFIG_DIR=./config backd types --realm shelf --functions
```

writes a `backd.d.ts` into each database's `_functions/` folder. A function wraps its handler in `defineFunction` and names itself `<database>/<function>`; the context is then typed from its schemas and `function.yaml`:

{{< example-file path="shelf/main/_functions/maintenance-open/index.ts" >}}

`ctx.admin.config.set` exists only because `function.yaml` says `config: write`, and its keys are the configuration's paths: `"maintenance.enabld"` is an error. The same holds for `ctx.input` (from `input.schema.json`), `ctx.call` (only functions listed in `calls:`), `ctx.email` and `ctx.push` (only when declared) and the collections' documents. The name is checked at startup: a `defineFunction` that isn't the function's own location stops `backd` and says which name to use.

`defineFunction` returns its handler unchanged, so tests call it like before. `deno.json` maps `@backd/functions` to the small module in the project's hidden `.backd/` folder, which is what `deno test` and the editor read; `backd functions build` needs no setup at all.

## Watch it follow a change

Run `backd` in dev mode and edit a schema:

```sh
CONFIG_DIR=./config BACKD_DEV=true HTTP_ADDR=127.0.0.1:8080 backd serve
```

Add `"pinned": { "type": "boolean" }` to `config/shelf/main/assets/schema.json` and save. The log says `config reloaded` and `function types updated`: `backd.d.ts` has `pinned` in `MainAssets`, so `asset.pinned` type-checks in `export` with no restart. A schema that doesn't validate is refused instead, the previous config keeps serving, and the realm's configuration view in the admin UI shows why.

## The stale-types warning

The app on localhost sends the fingerprint of the `realm.json` it was generated from. The schema change above made the app's types out of date, so the next request is answered with `X-Backd-Types-Stale` and the console says:

```text
backd: the generated types are out of date with the server: run `backd types` and commit the result
```

Nothing is refused, and a production build sends no header. Regenerate:

```sh
backd types --realm shelf --out clients/js/examples/shelf/backd
backd types --realm shelf --diff --out clients/js/examples/shelf/backd    # before regenerating
```

`--diff` is what a pull request reviewer wants: one line per field added, removed or changed, function input changed or collection removed:

```text
+ field pinned of main/assets: boolean
```

## Keep them current in CI

`--check` writes nothing and exits non-zero when the files are missing or stale, naming what changed:

```sh
backd types --realm shelf --out clients/js/examples/shelf/backd --check
backd types --realm shelf --functions --check
```

Shelf's CI runs both (`make shelf-types`; `make shelf-types-update` regenerates), so a schema change can't merge without its types.

## You should see

- `backd types` writes `types.d.ts` and `realm.json`; run again, nothing changes.
- A wrong field, collection or function input in `app.js`, and a wrong configuration key in a function, are errors in an editor.
- With `BACKD_DEV=true`, saving a schema updates `backd.d.ts` without a restart; an invalid one is refused and the old config keeps serving.
- The app's console warns once when its types are out of date; `--check` fails and `--diff` lists the change.

Read [Generated types](../../clients/types/) for the mapping from JSON Schema, and [Typed functions](../../functions/writing/#typed-functions) for the context.
