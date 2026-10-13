---
title: "Generated types"
description: "backd types: TypeScript types for a realm's documents, queries, functions, configuration, templates and roles, generated from the config the server enforces."
icon: "code"
weight: 640
toc: true
---

A realm's config already describes its API: collections and their schemas, functions and their inputs, app configuration, templates and roles. `backd types` turns it into one TypeScript file, so editors and compilers know what the API holds. Nothing is checked at runtime from the generated types; the server validates every request regardless.

```sh
CONFIG_DIR=./config backd types --realm shop --out src/backd
# wrote src/backd/types.d.ts and src/backd/realm.json (types 3f9a0c61d2b7e485)
```

`--from https://api.example.com` reads the config of a running instance instead of `CONFIG_DIR` (an administrator of the realm, with the `config` area: [the admin API](../../auth/admin/)). `--db main` limits the output to one database.

## What it writes

| File | Contents |
|---|---|
| `types.d.ts` | The types, with a header naming the generator version, the realm and the fingerprint of the types |
| `realm.json` | The fingerprint, the generator version, the config fingerprint and one entry per field, function, setting, template and role: what `--diff` compares |

Everything hangs off one `Realm` interface, addressed by path, so no naming rules are needed:

```ts
import type { Realm } from "./backd/types";

type Order = Realm["db"]["main"]["collections"]["orders"]["doc"];
type PlaceInput = Realm["db"]["main"]["functions"]["place"]["input"];
type Currency = Realm["config"]["paths"]["shop.currency"];
```

Friendly aliases are generated too, for every collection: `MainOrders` (the document), `MainOrdersInput` (what a client may write), `MainOrdersWhere`, `MainOrdersOrderBy`. When two names would collide, the alias is left out, with a warning, and the path still works.

| Part | Type |
|---|---|
| `db.<database>.collections.<name>.doc` | The stored document: `id`, `_meta` and the fields of `schema.json`. [File fields](../../files/file-fields/) are typed as `FileDetails` |
| `…​.input` | What a client sends: the same without `id`, `_meta` and file fields (only uploads write files) |
| `…​.where` | Every queryable field with the operators its type allows: `$eq`, `$ne`, `$in`, `$nin`, `$isNull` and `$notNull` for all; `$gt`, `$gte`, `$lt`, `$lte` and `$between` for strings, numbers and dates; the text operators for strings; and `$or` and `$and` of the same type |
| `…​.order_by` | A field, `-field`, or a comma separated list of them |
| `db.<database>.functions.<name>` | `input` and `output` from `input.schema.json` and `output.schema.json` (`unknown` without them), `mode`, `internal` and the `calls` the function declares |
| `config.tree`, `config.paths` | The app configuration as one object, and the type of each value by its dot path |
| `templates.email`, `templates.push` | The kinds that have templates; their data is `Record<string, unknown>` until templates declare data schemas |
| `roles` | The role names of `realm.yaml` as a union |

## From JSON Schema to TypeScript

| JSON Schema | TypeScript |
|---|---|
| `object` with `properties` and `required` | an object type with required and optional members |
| `additionalProperties: false` | a closed object type; otherwise it also accepts other `unknown` keys |
| `additionalProperties: {…}` and no `properties` | `Record<string, T>` |
| `array` with `items`, or `prefixItems` | `T[]`, or a tuple |
| `enum`, `const` | literal unions |
| `type: ["string", "null"]` | `string \| null` |
| `integer`, `number` | `number` |
| `oneOf`, `anyOf`, `allOf` | a union, or an intersection |
| `$ref` to `#/$defs/…` | a named type, `MainOrders_Address` |
| `format: date-time` | `DateTimeString`, a branded `string` |

Keywords that only validate (`minimum`, `pattern`, `maxLength`, other `format` values) don't change the type. A schema that uses `not`, `if`/`then`/`else`, `patternProperties`, `propertyNames`, `dependentSchemas`, `unevaluatedProperties`, `unevaluatedItems` or `contains`, or a `$ref` that isn't local, is typed `unknown`, and the generator prints a warning with the file and the place.

## Keeping the types current

- **`--check`** writes nothing and exits non-zero when the files are missing or differ from what the config produces now, printing what changed. Run it in CI.
- **`--diff`** writes nothing and prints type-level changes since the files were generated, one per line: `+` added, `-` removed, `~` changed. It is what a pull request review wants to see:

```text
+ field done of main/orders: boolean
~ field total of main/orders: number → number | null
- collection main/archive
~ function main/place input: { items: string[] } → { items: string[]; coupon?: string }
```

{{< hint style="tip" title="Best practice" >}}
Commit `types.d.ts` and `realm.json`, and add `backd types --realm <realm> --out <dir> --check` to CI next to `backd config check`, so a schema change can't merge without its types.
{{< /hint >}}

The same document is available from a running instance at `GET /v1/{realm}/_admin/types` ([API reference](../../api/)).
