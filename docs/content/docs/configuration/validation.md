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

## Finding documents that no longer match

A schema is enforced when a document is **written**. Documents stored before you changed the schema are not touched, and MongoDB is set to leave them alone (`validationLevel: moderate`): a collection can hold documents that would be refused today. A *schema check* lists them. It reads every stored document, validates it with the same full validator the API uses on a write (so it also sees the keywords MongoDB's translation leaves out, such as `format`), and reports the ones that fail. It **never writes** anything.

{{< hint warning >}}
**A check reads every document of the collection, so on a big collection it takes a long time and loads MongoDB.** It runs in the background on a [worker](../../functions/running/#running-the-worker) (one check per realm at a time) and you can leave the page or the terminal; the admin UI and the CLI say so before they start it.
{{< /hint >}}

```sh
backd data check --realm shop                                  # every collection of the realm
backd data check --realm shop --database main --collection orders --limit 200
backd data checks --realm shop                                  # the latest report of every collection
backd data checks --realm shop --database main --collection orders   # one report, in full
```

`backd data check` starts the check, follows it on stderr (`main/orders 35% (scanned 3500 of about 10000 documents)`), prints each collection's report and **exits `1` when it found drift** (or when a collection was not read to the end), so a CI job or a deploy script can gate on it. `--no-wait` only starts it and prints the job id; `--json` prints one object per collection.

The same is in the [admin UI](../../auth/admin-ui/#data) (a **Check documents against the schema** button on a collection's page, which asks first, and a *Schema checks* page with every collection's latest report), the [admin API](../../auth/admin/#endpoints) (`POST /_admin/data-checks`) and the JavaScript client (`client.admin.dataChecks.start()` and `.wait()`).

### What it does

- **Reads in `_id` order, in batches,** every document: also those in the [trash](../../api/documents/#soft-delete) (the report flags them) and documents written while it runs may or may not be seen. The report says which schema it checked against (`schema_hash`).
- **Progress is a job's steps:** one step per collection, named after it, with a total of 100, advancing every 5 percent from the documents read out of the collection's size when the check began (`5, 10, 15 … 100`). It shows on the Jobs page, in `backd functions jobs --realm R --job ID`, and is how the CLI and the UI show the percentage.
- **Only one at a time per realm.** Starting another while one is queued or running answers `409 check_running` with the running job's id. It ends when it finishes, fails or is cancelled (the Jobs page, `backd functions cancel` and `POST /_admin/jobs/{id}/cancel` cancel it; a worker notices after the batch it is on and stops).
- **It stops early, and says so:** a collection's scan ends when it has found `limit` invalid documents (default 100, at most 1000), and any scan ends after 6 hours. The report then has `complete: false` and `stopped_by` (`limit` or `time`); fix what it listed and check again.
- **A worker must be running.** Without one the check waits in the queue. If a worker dies, another picks the job up after its lease (10 minutes) and starts over.

### The report

Each collection keeps **the latest report**, replaced by the next check that finishes (a cancelled or failed check leaves the previous one), with no expiry, so users can read it whenever. For each of up to `limit` invalid documents it holds:

```json
{
  "id": "d3c9lg38hc2g00b6s1lg",
  "deleted": false,
  "problems": [
    { "path": "items.2.price", "reason": "got string, want number" },
    { "path": "status", "reason": "is required" }
  ],
  "more_problems": 0
}
```

- A document lists at most 5 problems (`more_problems` counts the rest), each a JSON path (dots between names, empty for the document itself) and the rule that failed. **The values are never in the report**, so it is safe to share or log. A path can contain a property name taken from the document (for `additionalProperties: false`).
- Who: the `data` [admin area](../../auth/admin/#admin-rights), **read is enough** to start, cancel and read a check (for the read-only level, `admin.read_access.data: true`). A level without `data` gets `403`. Starting one is audited as `data.check`.

### Upgrading

Reports live in a new system collection, `schema_checks`, in the realm's system database: run `backd provision` (or start with `PROVISION_MODE=apply`) after upgrading, as for any new system collection.

## System fields

`backd` injects its own fields into every database validator:

- `_id`, a string;
- `_meta`, an object with `created_at` and `updated_at` dates and an integer `version` of at least 1, plus the optional [ownership fields](../../api/documents/#document-shape): `owner` (a string or `null`), `created_by` and `updated_by` (strings).

`_id` and `_meta` are added to `required`. As a result, schemas that use `"additionalProperties": false` keep working, even though stored documents always carry these fields.

## Numbers

JSON numbers in request bodies are stored as 64-bit integers when they're whole and fit in 64 bits, however they're written: `3`, `3.0` and `3e0` are all stored as the integer 3, as JSON Schema counts them all as integers. All other numbers are stored as doubles. Whole numbers too large for 64 bits are also stored as doubles, which is why the `integer` translation admits doubles (still required to be whole); they may lose precision, so keep integers within ±9,223,372,036,854,775,807.

Every document the API accepts is also accepted by the database validator. A test checks exactly that against a real MongoDB, on hundreds of documents, valid and invalid, generated for a test schema and for every sample schema in the repository.
