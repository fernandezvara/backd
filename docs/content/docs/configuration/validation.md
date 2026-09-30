---
title: "Schema validation"
description: "How schemas are enforced by the API and mirrored in MongoDB."
icon: "rule"
weight: 220
toc: true
---

Every collection's `schema.json` is enforced at two levels.

1. **API level (authoritative).** Every write is validated against the full JSON Schema draft 2020-12 before it reaches the database. Failures return `400` with one entry per failing field.
2. **Database level (safety net).** The same schema is translated into a MongoDB `$jsonSchema` collection validator. This protects the data from writes that bypass the API. MongoDB's dialect is older and smaller, so the translation covers a subset of the schema.

## Translation to MongoDB

| JSON Schema | MongoDB `$jsonSchema` |
|---|---|
| `type: string` / `boolean` / `object` / `array` / `null` | `bsonType: string` / `bool` / `object` / `array` / `null` |
| `type: integer` | `bsonType: [int, long, double]` plus `multipleOf: 1`, unless the schema sets its own `multipleOf` |
| `type: number` | `bsonType: [int, long, double, decimal]` |
| `const: v` | `enum: [v]` |
| `exclusiveMinimum: n` / `exclusiveMaximum: n` | `minimum` / `maximum: n` plus `exclusiveMinimum` / `exclusiveMaximum: true` |
| `properties`, `required`, `enum`, `minimum`, `maximum`, `multipleOf`, `minLength`, `maxLength`, `pattern`, `items`, `minItems`, `maxItems`, `uniqueItems`, `additionalProperties`, `minProperties`, `maxProperties`, `allOf`, `anyOf`, `oneOf`, `not` | copied as-is (subschemas translated recursively) |

- Annotations (`title`, `description`, `default`, `examples`, `$comment`, …) are dropped silently.
- Any other keyword (for example `format`, `$ref`/`$defs`, `prefixItems`, `unevaluatedProperties` or `if`/`then`/`else`) is left out of the database validator and logged at `info` level with its location. The API level still enforces it.

## System fields

`backd` injects its own fields into every database validator:

- `_id`, a string;
- `_meta`, an object with `created_at` and `updated_at` dates and an integer `version` of at least 1, plus the optional [ownership fields](../../api/documents/#document-shape): `owner` (a string or `null`), `created_by` and `updated_by` (strings).

`_id` and `_meta` are added to `required`. As a result, schemas that use `"additionalProperties": false` keep working, even though stored documents always carry these fields.

## Numbers

JSON numbers in request bodies are stored as 64-bit integers when they're whole and fit in 64 bits, however they're written: `3`, `3.0` and `3e0` are all stored as the integer 3, as JSON Schema counts them all as integers. All other numbers are stored as doubles. Whole numbers too large for 64 bits are also stored as doubles, which is why the `integer` translation admits doubles (still required to be whole); they may lose precision, so keep integers within ±9,223,372,036,854,775,807.

Every document the API accepts is also accepted by the database validator. A test checks exactly that against a real MongoDB, on hundreds of documents, valid and invalid, generated for a test schema and for every sample schema in the repository.
