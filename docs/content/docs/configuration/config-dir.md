---
title: "Data model (CONFIG_DIR)"
description: "Declare realms, databases and collections with schema.json and indexes.json."
icon: "folder_open"
weight: 210
toc: true
---

The data model is a directory tree under `CONFIG_DIR`, loaded once at startup. Directories define realms and databases, and a `schema.json` defines a collection.

```
$CONFIG_DIR/<realm>/
    realm.yaml           # required — realm settings
    email/<kind>/        # optional — email templates (see Functions → Email)
    <database>/<collection>/
        schema.json      # required — JSON Schema draft 2020-12
        indexes.json     # optional — index declarations
        rules.yaml       # optional — access rules (see Authentication)
    <database>/_functions/  # optional — server-side functions
```

- Each directory under `CONFIG_DIR` is a **realm**. It must contain a [`realm.yaml`](../realm/).
- Each directory inside a realm is a **database**, stored in the MongoDB database `<realm>__<database>`.
- Each directory inside a database is a **collection**, stored in a MongoDB collection of the same name. It must contain a `schema.json`.
- A realm's `email` folder holds its [email templates](../../functions/email/#templates), and `pages` is reserved for the pages backd will serve; neither is a database, so they are never read as one and can't be database names.
- A database's `_functions` directory holds its [functions](../../functions/), not a collection.
- Other plain files at the realm and database levels are ignored, as are hidden entries (names starting with `.`).
- Changing the config requires a restart. There is no hot-reload.

## Starting from a template

`backd template` writes commented starter files, so you don't have to start from an empty directory. It only needs `CONFIG_DIR`.

{{< hint style="tip" title="Already done for you" >}}
`backd template` never overwrites an existing file, so it is safe to run again: it lists what it created and what it left alone. Start from `--sample` to get a working collection with commented schema, indexes and rules, then delete what you don't need.
{{< /hint >}}

```sh
export CONFIG_DIR=./config
backd template realm --realm demo              # demo/realm.yaml
backd template database --realm demo --database cms       # the demo/cms/ directory
backd template database --realm demo --database blog --sample
backd template function --realm demo --database blog --name stats  # a function, see Functions
backd template email-capture --realm demo --database blog          # a development email function, see Functions -> Email
```

With `--sample`, the command adds a `posts` collection (title, body, published, optional category, author) with explanations: `schema.json`, `indexes.json` and a commented [`rules.yaml`](../../auth/rules/) (anyone reads published posts, signed-in users write posts signed with their own email, authors edit and delete their own but can't change who wrote them). `backd template realm --realm demo --sample` creates the realm plus a database named `main` holding that sample: the same database as the `blog` realm in `examples/config` (whose `realm.yaml` is adjusted for the example app).

The command prints each file it created and each one it left unchanged because it already existed.

For a complete config repository in one step — the realm and sample database above, a sample function with tests, Dockerfiles and a local development stack (MongoDB, backd, the functions executor and egress), and a GitHub Actions CI workflow — use `backd template project --dir <directory> --realm <realm>` instead. Unlike the commands above, `<directory>` must not exist yet: it creates the whole tree, not just a piece of it. See [Deploying config](../../operations/deploying/) for the shape it follows and [Unit testing a function's logic](../../functions/testing/#unit-testing-a-functions-logic) for the testing helper it vendors.

```sh
backd template project --dir ./my-app --realm demo
cd my-app && cat README.md
```

## Names

Realm, database and collection names must match `^[a-z0-9]+(?:[-_][a-z0-9]+)*$`: lowercase letters and digits, optionally separated by single `-` or `_`.

- Names can't start with `_`, which is reserved for system routes and fields.
- Names can't contain `__`, which separates realm and database in MongoDB names.
- `<realm>__<database>` must be shorter than 64 characters.
- A realm name can be at most 54 characters, so that its system database `<realm>___system` fits MongoDB's limit too.

## `schema.json`

- Must be a valid JSON Schema object. `$schema` may be omitted and is then treated as draft 2020-12. Any other draft is rejected.
- `format` keywords are asserted: for example, `"format": "email"` rejects values that aren't email addresses.
- Must not declare or require `id`, `_id` or `_meta`. These are system-owned fields that `backd` adds to every document, and clients can never set them.

Example, `shop/orders/items/schema.json`:

```json
{
  "type": "object",
  "properties": {
    "name":   { "type": "string", "minLength": 1 },
    "price":  { "type": "number", "minimum": 0 },
    "status": { "enum": ["active", "archived"] }
  },
  "required": ["name", "price"],
  "additionalProperties": false
}
```

## `indexes.json`

This optional file declares the collection's MongoDB indexes:

```json
[
  { "fields": ["status", "-_meta.created_at"] },
  { "fields": ["email"], "unique": true }
]
```

- `fields` lists one or more fields in `order_by` notation: a `-` prefix means descending. Each field must be declared in `schema.json` or be `id`, `_meta.created_at`, `_meta.updated_at`, `_meta.version`, `_meta.owner`, `_meta.created_by` or `_meta.updated_by`. An index on `_meta.owner` keeps "owner only" access rules fast.
- `unique` (default `false`) rejects documents that repeat a value. A write that violates it returns `409 conflict`.
- Unknown keys, duplicate fields within an index, and two indexes with the same fields are all rejected.

{{< hint warning >}}
A document without a uniquely indexed field counts as having `null` there, so only **one** document may lack it: the second is refused with `409`. Make every field with a unique index `required` in `schema.json`. And remember that a unique index tells callers that a value exists, even if its document is hidden by a [rule](../../auth/rules/).
{{< /hint >}}

Indexes are created at startup by [provisioning](../../operations/). Indexes that exist in MongoDB but aren't declared here are kept and reported as warnings. An index on `id` alone is unnecessary, because MongoDB always indexes `_id`.

{{< hint style="tip" title="Best practice" >}}
Filtering and sorting on unindexed fields works, but scans the whole collection, which is fine for a few thousand documents and slow beyond that. Add an index for every query your application runs often, and for `_meta.owner` when [rules](../../auth/rules/) restrict access to the owner.
{{< /hint >}}

## Errors

Any invalid config stops startup with a non-zero exit. Every error is reported at once, each naming the offending file or directory. Examples of invalid config:

- a missing `CONFIG_DIR`;
- a realm without `realm.yaml`, or an invalid `realm.yaml` (see [Realm settings](../realm/#validation));
- an invalid name;
- a collection directory without `schema.json`;
- invalid JSON or an invalid schema;
- a schema that declares a system field;
- an invalid `indexes.json`;
- an invalid [`rules.yaml`](../../auth/rules/#checks-at-startup).
