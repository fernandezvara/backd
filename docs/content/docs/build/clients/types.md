---
title: "Generated types"
description: "backd types: TypeScript types for a realm's documents, queries, functions, configuration, templates and roles, generated from the config the server enforces."
icon: "code"
weight: 640
toc: true
---

A realm's config already describes its API: collections and their schemas, functions and their inputs, app configuration, templates and roles. `backd types` turns it into one TypeScript file — or one Dart file with `--lang dart`, one Go file with `--lang go`, or one Python file with `--lang python` — so editors and compilers know what the API holds. Nothing is checked at runtime from the generated types; the server validates every request regardless.

```sh
CONFIG_DIR=./config backd types --realm shop --out src/backd
# wrote src/backd/types.d.ts and src/backd/realm.json (types 3f9a0c61d2b7e485)

CONFIG_DIR=./config backd types --realm shop --lang dart --out lib/backd
# wrote lib/backd/types.dart and lib/backd/realm.json (types 3f9a0c61d2b7e485)

CONFIG_DIR=./config backd types --realm shop --lang go --package shopapi --out shopapi
# wrote shopapi/types.go and shopapi/realm.json (types 3f9a0c61d2b7e485)
CONFIG_DIR=./config backd types --realm shop --lang python --out shop_api
# wrote shop_api/types.py and shop_api/realm.json (types 3f9a0c61d2b7e485)
```

The fingerprint is the same for every language: they describe the same realm.

`--from https://api.example.com` reads the config of a running instance instead of `CONFIG_DIR` (an administrator of the realm, with the `config` area: [the admin API](../../../operate/administration/admin/)). `--lang` (and `--package`, for Go) travels with it (`?lang=python` asks the server's [types endpoint](../../api/) for Dart). `--db main` limits the output to one database.

## What it writes

| File | Contents |
|---|---|
| `types.d.ts` (`--lang ts`, the default) | The TypeScript types, with a header naming the generator version, the realm and the fingerprint of the types |
| `types.dart` (`--lang dart`) | The same types for Dart: classes with `fromJson`/`toJson`, plus a `ShopRealm` accessor and an extension on `BackdClient<ShopRealm>` for the [Dart client](../dart/) |
| `types.py` (`--lang python`) | The same types for Python: a `TypedDict` per document, write input and function input or output, `Literal` types for enums, classes of constants for the fields of `where` and `order_by`, plus a `ShopRealm` (and an `AsyncShopRealm`) over the [Python client](../python/) with typed collections and function calls |
| `types.go` (`--lang go`) | The same types for Go: structs with JSON tags, string types with constants for enums, constants for the fields of `where` and `order_by`, plus a `ShopRealm` over the [Go client](../go/) with typed collections and function calls. `--package` names the package (`backdtypes` by default) |
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

| JSON Schema | TypeScript | Dart | Go | Python |
|---|---|---|---|---|
| `object` with `properties` and `required` | an object type with required and optional members | a class with required and nullable fields, `fromJson`/`toJson` | a struct with JSON tags: required fields by value, optional ones as pointers (`omitempty`) | a `TypedDict`: required fields in one class, optional ones in another |
| `additionalProperties: false` | a closed object type; otherwise it also accepts other `unknown` keys | a closed class | a struct; unknown keys are dropped when decoding | a `TypedDict`; unknown keys are not stopped |
| `additionalProperties: {…}` and no `properties` | `Record<string, T>` | `Map<String, T>` | `map[string]T` | `dict[str, T]` |
| `array` with `items`, or `prefixItems` | `T[]`, or a tuple | `List<T>` | `[]T` (a tuple is `[]any`) | `list[T]`, or `tuple[A, B]` |
| `enum`, `const` | literal unions | an `enum`, or a `String` constant | a string type with a constant per value | `Literal["a", "b"]` |
| `type: ["string", "null"]` | `string \| null` | `String?` | `*string` | `str \| None` |
| `integer`, `number` | `number` | `int`, `num` | `int64`, `float64` | `int`, `float` |
| `oneOf`, `anyOf`, `allOf` | a union, or an intersection | a sealed class hierarchy | `json.RawMessage`, with a warning | a union, `A \| B` (`allOf`: `Any`) |
| `$ref` to `#/$defs/…` | a named type, `MainOrders_Address` | a named class, `MainOrdersAddress` | a named struct, `MainOrdersAddress` | a named class, `MainOrdersAddress` |
| `format: date-time` | `DateTimeString`, a branded `string` | `DateTimeString`, a Dart 3 extension type on `String` | `time.Time` | `str` (RFC 3339) |

Keywords that only validate (`minimum`, `pattern`, `maxLength`, other `format` values) don't change the type. A schema that uses `not`, `if`/`then`/`else`, `patternProperties`, `propertyNames`, `dependentSchemas`, `unevaluatedProperties`, `unevaluatedItems` or `contains`, or a `$ref` that isn't local, is typed `unknown` (`Any` in Python, `any` in Go, `Object?` in Dart), and the generator prints a warning with the file and the place.

## Organization roles

In a realm with [organizations](../../organizations/api/) the generated `Realm` has `org_roles`, the union of the roles declared under `organizations.roles` (`"owner" | "admin" | "editor" | "member"`). Use it to type what an app gives: `const roles: Realm["org_roles"][] = ["editor"]`. Collections that belong to organizations list `_meta.org` among the fields of `where` and `orderBy`, and `inTeam(id)` keeps the types of the collection.

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

## In Go

For the [Go client](../go/): `--lang go` writes `types.go`, whose `ShopRealm` (named for the realm) binds the types to a `*client.Client`. A document is a struct that embeds `client.Base` (`ID`, `Meta`); `Typed` collections read into it and write the `Input` struct:

```go
import "example.com/app/shopapi"   // generated

shop := shopapi.NewShopRealm(c)
orders := shop.Main().Orders()     // *client.Typed[shopapi.MainOrders]

page, err := orders.List(ctx, client.Query{
    Where:   client.Where{shopapi.MainOrdersFieldStatus: "paid"},
    OrderBy: []string{shopapi.MainOrdersFieldMetaCreatedAtDesc},
})
order, err := orders.Create(ctx, shopapi.MainOrdersInput{Status: shopapi.MainOrdersStatusNew, Items: items})
out, err := shop.Main().Place(ctx, shopapi.FnMainPlaceInput{Items: items})   // a typed function call
```

An optional field is a pointer with `omitempty`, so a zero value can be told from an absent one; an optional list or map left empty is not sent. A schema Go can't express (`oneOf`, a union of JSON types) is typed `json.RawMessage` or `any`, with a warning. The file is marked `Code generated … DO NOT EDIT.`, so linters skip it.

## In Python

For the [Python client](../python/): `--lang python` writes `types.py` (put it in your own package: `from shop_api.types import …`). A document is a `TypedDict` that extends `backd.Doc` (`id`, `_meta`); `ShopRealm` binds the types to a `Client`, and `AsyncShopRealm` to an `AsyncClient`:

```python
from shop_api.types import MainOrdersFields, MainOrdersInput, ShopRealm, FnMainPlaceInput

shop = ShopRealm(client)
orders = shop.main.orders                      # backd.Collection[MainOrders]

page = orders.list(where={MainOrdersFields.STATUS: "paid"}, order_by=MainOrdersFields.META_CREATED_AT_DESC)
first = page.items[0]["status"]                # the checker knows it is "new" | "paid" | "shipped"
order = orders.create(MainOrdersInput(status="new", items=items))
out = shop.main.place({"items": ["sku-1"]})    # a typed function call
```

Python 3.10 has no `NotRequired`, so each object is two `TypedDict`s (the required fields, the optional ones) joined by a third; a property name Python can't write (`from`, `my-field`) goes in a `TypedDict` made the functional way. Run `mypy --strict` over the file together with your code: the generated file is checked against the client in this repository's CI.

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

The [JavaScript client](../js/#typed-client) takes the `Realm` interface as `createClient<Realm>()`; the [Dart client](../dart/#typed-client) takes `ShelfRealm` as `BackdClient<ShelfRealm>()` and `client.typed`. For functions, `--functions` writes each functions project's `backd.d.ts` instead, with `defineFunction` ([Typed functions](../../functions/writing/#typed-functions)). The same types are available from a running instance at `GET /v1/{realm}/_admin/types` (`?lang=dart` for Dart, `?lang=go&package=name` for Go, `?lang=python` for Python; [API reference](../../api/)), and the realm's [OpenAPI document](../../api/openapi/) is the way to type clients in other languages.
