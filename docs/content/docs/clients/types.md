---
title: "Generated types"
description: "backd types: TypeScript types for a realm's documents, queries, functions, configuration, templates and roles, generated from the config the server enforces."
icon: "code"
weight: 640
toc: true
---

A realm's config already describes its API: collections and their schemas, functions and their inputs, app configuration, templates and roles. `backd types` turns it into one TypeScript file — or one Dart file, with `--lang dart` — so editors and compilers know what the API holds. Nothing is checked at runtime from the generated types; the server validates every request regardless.

```sh
CONFIG_DIR=./config backd types --realm shop --out src/backd
# wrote src/backd/types.d.ts and src/backd/realm.json (types 3f9a0c61d2b7e485)

CONFIG_DIR=./config backd types --realm shop --lang dart --out lib/backd
# wrote lib/backd/types.dart and lib/backd/realm.json (types 3f9a0c61d2b7e485)
```

The fingerprint is the same for both languages: they describe the same realm.

`--from https://api.example.com` reads the config of a running instance instead of `CONFIG_DIR` (an administrator of the realm, with the `config` area: [the admin API](../../auth/admin/)). `--lang` travels with it (`?lang=dart` asks the server's [types endpoint](../../api/) for Dart). `--db main` limits the output to one database.

## What it writes

| File | Contents |
|---|---|
| `types.d.ts` (`--lang ts`, the default) | The TypeScript types, with a header naming the generator version, the realm and the fingerprint of the types |
| `types.dart` (`--lang dart`) | The same types for Dart: classes with `fromJson`/`toJson`, plus a `ShopRealm` accessor and an extension on `BackdClient<ShopRealm>` for the [Dart client](../dart/) |
| `realm.json` | The fingerprint, the generator version, the config fingerprint and one entry per field, function, setting, template and role: what `--diff` compares |

Everything hangs off one `Realm` interface, addressed by path, so no naming rules are needed:

```ts
import type { Realm } from "./backd/types";

type Order = Realm["db"]["main"]["collections"]["orders"]["doc"];
type PlaceInput = Realm["db"]["main"]["functions"]["place"]["input"];
type Currency = Realm["config"]["paths"]["shop.currency"];
```

Friendly aliases are generated too, for every collection: `MainOrders` (the document), `MainOrdersInput` (what a client may write; `MainOrdersCreate` for a collection with custom ids), `MainOrdersWhere`, `MainOrdersOrderBy`. The Dart file uses the same names — `MainOrders` is a class there — and adds `MainOrdersField` constants for `where` and `orderBy`. When two names would collide, the alias is left out, with a warning, and the path still works.

| Part | Type |
|---|---|
| `db.<database>.collections.<name>.doc` | The stored document: `id`, `_meta` and the fields of `schema.json`. [File fields](../../files/file-fields/) are typed as `FileDetails` |
| `…​.input` | What a client sends: the same without `id`, `_meta` and file fields (only uploads write files) |
| `…​.create` | What a client sends to create: `input`, plus the required `id` in a collection with [`custom_id`](../../configuration/config-dir/#custom-ids) (`MainInvoicesCreate`). The same as `input` elsewhere |
| `…​.where` | Every queryable field with the operators its type allows: `$eq`, `$ne`, `$in`, `$nin`, `$isNull` and `$notNull` for all; `$gt`, `$gte`, `$lt`, `$lte` and `$between` for strings, numbers and dates; the text operators for strings; and `$or` and `$and` of the same type |
| `…​.order_by` | A field, `-field`, or a comma separated list of them |
| `db.<database>.functions.<name>` | `input` and `output` from `input.schema.json` and `output.schema.json` (`unknown` without them), `mode`, `internal` and the `calls` the function declares |
| `config.tree`, `config.paths` | The app configuration as one object, and the type of each value by its dot path |
| `templates.email`, `templates.push` | The kinds that have templates; their data is `Record<string, unknown>` until templates declare data schemas |
| `roles` | The role names of `realm.yaml` as a union |

## From JSON Schema to TypeScript

| JSON Schema | TypeScript | Dart |
|---|---|---|
| `object` with `properties` and `required` | an object type with required and optional members | a class with required and nullable fields, `fromJson`/`toJson` |
| `additionalProperties: false` | a closed object type; otherwise it also accepts other `unknown` keys | a closed class |
| `additionalProperties: {…}` and no `properties` | `Record<string, T>` | `Map<String, T>` |
| `array` with `items`, or `prefixItems` | `T[]`, or a tuple | `List<T>` |
| `enum`, `const` | literal unions | an `enum`, or a `String` constant |
| `type: ["string", "null"]` | `string \| null` | `String?` |
| `integer`, `number` | `number` | `int`, `num` |
| `oneOf`, `anyOf`, `allOf` | a union, or an intersection | a sealed class hierarchy |
| `$ref` to `#/$defs/…` | a named type, `MainOrders_Address` | a named class, `MainOrdersAddress` |
| `format: date-time` | `DateTimeString`, a branded `string` | `DateTimeString`, a Dart 3 extension type on `String` |

Keywords that only validate (`minimum`, `pattern`, `maxLength`, other `format` values) don't change the type. A schema that uses `not`, `if`/`then`/`else`, `patternProperties`, `propertyNames`, `dependentSchemas`, `unevaluatedProperties`, `unevaluatedItems` or `contains`, or a `$ref` that isn't local, is typed `unknown`, and the generator prints a warning with the file and the place.

## Organization roles

In a realm with [organizations](../../api/organizations/) the generated `Realm` has `org_roles`, the union of the roles declared under `organizations.roles` (`"owner" | "admin" | "editor" | "member"`). Use it to type what an app gives: `const roles: Realm["org_roles"][] = ["editor"]`. Collections that belong to organizations list `_meta.org` among the fields of `where` and `orderBy`, and `inOrg(id)` keeps the types of the collection.

## In TypeScript and in plain JavaScript

Apps and functions get the same types in both languages; JavaScript needs no build step, because editors read the JSDoc:

```ts
// TypeScript
import type { Realm } from "./backd/types";
const client = createClient<Realm>({ url, realm: "shop" });
```

```js
// JavaScript, checked in the editor (or by tsc --checkJs)
/** @typedef {import('./backd/types').Realm} Realm */
/** @type {import('backd-js').TypedClient<Realm>} */
const client = /** @type {any} */ (createClient({ url, realm: 'shop' }));
```

```js
// A function in plain JavaScript: // @ts-check turns on the same checks as in TypeScript.
// @ts-check
import { defineFunction } from "@backd/functions";
export default defineFunction("main/place", async (ctx) => ({ order_id: ctx.input.items[0] }));
```

## In Dart

The same generation for the [Dart client](../dart/): `--lang dart` writes `types.dart`, whose `ShelfRealm` (named for the realm) types `client.db`, collections, functions and configuration, and whose `ShelfRealmAccess` extension puts it on `BackdClient<ShelfRealm>`:

```dart
import 'package:backd/backd.dart';
import 'backd/types.dart';   // generated

final client = BackdClient<ShelfRealm>(url: url, realm: 'shelf');

final page = await client.typed.main.assets.list(
  where: {MainAssetsField.publishedAt: {'\$ne': null}},
  orderBy: '-${MainAssetsField.publishedAt}',
);
page.items[0].kind;                                   // MainAssetsKind.link | note | file
await client.typed.main.publish(FnMainPublishInput(assetId: id));
```

Every collection gets a `TypedCollection<D, I, C>` wrapper — reads decode into the document class, `create`/`replace` encode its `Input` type, `.raw` reaches the plain collection — and every function a typed call. [The Dart client guide](../dart/#typed-client) has the whole picture.

## Keeping the types current (drift)

Types are only useful while they match the config. backd gives three ways to notice when they don't, from the strictest to the softest:

| Where | What |
|---|---|
| **CI** | `--check` fails when the generated files are missing or differ from what the config produces now, naming what changed |
| **Pull request reviews** | `--diff` prints the type-level changes since the files were generated |
| **Development** | in dev mode the function types are rewritten after every reload, and the app's [stale-types warning](../js/#typed-client) tells you when its copy is out of date |

Nothing is enforced at run time: the server validates every request against the config whatever the client's types say.

`--diff` prints one line per change, `+` added, `-` removed, `~` changed:

```text
+ field done of main/orders: boolean
~ field total of main/orders: number → number | null
- collection main/archive
~ function main/place input: { items: string[] } → { items: string[]; coupon?: string }
```

A ready-made step for GitHub Actions (other CI systems: the same three commands):

```yaml
- name: Generated types are current
  run: |
    backd config check
    backd types --realm shop --out src/backd --check
    backd types --realm shop --functions --check
    backd openapi --realm shop --out openapi/shop.json --check   # if you commit the OpenAPI document
  env:
    CONFIG_DIR: ./config
```

When it fails, run the commands without `--check` and commit the result; the failure output lists what changed. The Shelf tutorial's CI does exactly this (`make shelf-types`).

{{< hint style="note" >}}
`realm.json`'s fingerprint covers the whole realm. The app's stale-types warning compares it with the types of the **whole** realm, so generate the app's types without `--db`; with it, the warning never goes away.
{{< /hint >}}

The [JavaScript client](../js/#typed-client) takes the `Realm` interface as `createClient<Realm>()`; the [Dart client](../dart/#typed-client) takes `ShelfRealm` as `BackdClient<ShelfRealm>()` and `client.typed`. For functions, `--functions` writes each functions project's `backd.d.ts` instead, with `defineFunction` ([Typed functions](../../functions/writing/#typed-functions)). The same types are available from a running instance at `GET /v1/{realm}/_admin/types` (`?lang=dart` for Dart; [API reference](../../api/)), and the realm's [OpenAPI document](../../api/openapi/) is the way to type clients in other languages.
