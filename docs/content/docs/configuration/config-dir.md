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
    app-config/          # optional — app configuration: schema.json and rules.yaml
    <database>/<collection>/
        schema.json      # required — JSON Schema draft 2020-12
        indexes.json     # optional — index declarations
        collection.yaml  # optional — access rules, file fields, soft delete, erase policy
        rules.test.yaml  # optional — tests of the rules (`backd rules test`)
    <database>/_functions/  # optional — server-side functions
```

- Each directory under `CONFIG_DIR` is a **realm**. It must contain a [`realm.yaml`](../realm/).
- Each directory inside a realm is a **database**, stored in the MongoDB database `<realm>__<database>`.
- Each directory inside a database is a **collection**, stored in a MongoDB collection of the same name. It must contain a `schema.json`.
- A realm's `email` folder holds its [email templates](../../functions/email/#templates), and `pages` is reserved for the pages backd will serve; neither is a database, so they are never read as one and can't be database names.
- A realm's `app-config` folder declares its [app configuration](#app-configuration), and is not a database either.
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
backd template collection-policy --realm demo --database blog --collection posts   # a commented collection.yaml
```

With `--sample`, the command adds a `posts` collection (title, body, published, optional category, author) with explanations: `schema.json`, `indexes.json` and a `collection.yaml` with commented [`rules:`](../../auth/rules/) (anyone reads published posts, signed-in users write posts signed with their own email, authors edit and delete their own but can't change who wrote them), and a [`collection.yaml`](#collectionyaml) that anonymizes the byline when a user is erased. `backd template realm --realm demo --sample` creates the realm plus a database named `main` holding that sample: the same database as the `blog` realm in `examples/config` (whose `realm.yaml` is adjusted for the example app).

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
- A `date-time` string can be stored as a real date: add `"x-backd-store": "date"` to its property (see [Dates](#dates)).
- Must not declare or require `id`, `_id` or `_meta`. These are system-owned fields that `backd` adds to every document. Clients can't set them; the one exception is the `id` of a create in a collection with [`custom_id`](#custom-ids).

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

### Dates

By default a `format: date-time` property is text: MongoDB stores the string, and comparing or sorting it as time only works when every value has the same form. Add `x-backd-store` to store it as a **BSON Date** instead, so queries, sorting and index ranges compare instants:

```json
"starts_at": { "type": "string", "format": "date-time", "x-backd-store": "date" },
"ends_at":   { "type": ["string", "null"], "format": "date-time", "x-backd-store": "date" }
```

- **Clients don't change.** They still send and read RFC 3339 strings, and the schema still validates them. In `where`, `$eq`, `$ne`, `$in`, `$gt`, `$gte`, `$lt`, `$lte` and `$between` take RFC 3339 strings in any offset and compare instants (`{"starts_at": {"$gte": "2026-09-01T00:00:00+02:00"}}`); text operators (`$like`, `$startsWith`…) are refused on them; `order_by` and [cursors](../../api/querying/#paging-with-a-cursor) work.
- **Precision.** MongoDB keeps dates in UTC to the millisecond, so `2026-09-01T14:00:00.123456+02:00` reads back as `2026-09-01T12:00:00.123Z`, like `_meta` timestamps.
- **Where it is allowed.** On a property (at the top or nested in objects) whose type is `string`, or `["string", "null"]`, with `"format": "date-time"` and none of `pattern`, `minLength`, `maxLength`, `enum` or `const`. Not inside arrays, `$defs`, or `allOf`/`anyOf`/`oneOf`/`not`: startup names the place and stops. The database validator then says `bsonType: date` for the field.
- **Rules** see these fields as times: they compare with `now` (`document.starts_at > now`, `data.ends_at > data.starts_at`) and with `nil`, never with text, which `backd` refuses at startup.
- **Existing data.** Turning this on for a field that already holds text doesn't convert it. Documents written before keep the text until they are written again (an update converts the field), and queries by date skip them until then. Convert them in one go with `mongosh`, for example `db.items.updateMany({starts_at: {$type: "string"}}, [{$set: {starts_at: {$toDate: "$starts_at"}}}])`, when you [provision](../../operations/deploying/) the new schema. The validator uses `validationLevel: moderate`, so documents that don't conform yet stay readable until they are written.

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

Indexes are created at startup by [provisioning](../../operations/). Indexes that exist in MongoDB but aren't declared here are kept and reported as warnings. An index on `id` alone is unnecessary, because MongoDB always indexes `_id`. Every collection also gets an index on `_meta.created_at, id` without being declared, which serves the [default order](../../api/querying/#ordering-and-pagination) of lists.

{{< hint style="tip" title="Best practice" >}}
Filtering and sorting on unindexed fields works, but scans the whole collection, which is fine for a few thousand documents and slow beyond that. Add an index for every query your application runs often, and for `_meta.owner` when [rules](../../auth/rules/) restrict access to the owner.
{{< /hint >}}

## `collection.yaml`

This optional file holds a collection's policy, in sections that can each be left out:

| Section | What it says | Documented in |
|---|---|---|
| `rules:` | Who may read, create, update and delete documents. **Without it only API keys reach the collection.** | [Access rules](../../auth/rules/) |
| `files:` | The fields of a document that carry files | [File fields](../../files/file-fields/) |
| `soft_delete:` | Whether deleted documents stay recoverable | [Soft delete](#soft-delete) |
| `cache:` | Whether anonymous reads may be stored by shared caches (CDNs) | [Public caching](#public-caching) |
| `personal_data:` | What a user's data export takes from the collection | [Personal data scope](#personal-data-scope) |
| `custom_id:` | Collections whose documents get the id the client chooses | [Custom ids](#custom-ids) |
| `organization:` | Whether documents belong to an organization, and what happens to them when it is deleted | [Organizations](#organizations) |
| `on_owner_delete:` | What an administrator's **erase** does to the collection when a user is erased (`backd user delete`) | below |

```yaml
rules:
  read: user != nil
  update: user != nil && document._meta.owner == user.id
soft_delete: true
```

`rules.test.yaml`, the fixture of [`backd rules test`](../../auth/rules/#testing-rules), stays a separate file next to it: it is a test, not configuration. A `rules.yaml` in a collection directory (where the rules used to be) stops `backd` from loading, with a message that says to move its content under `rules:`.

Without `on_owner_delete:` an erase leaves the collection alone: its documents keep everything, which is what you want for records you must keep, such as purchases. `backd template collection-policy` writes a commented example of all of the sections.

```yaml
on_owner_delete:
  action: anonymize          # delete | anonymize; leave it out to keep the user's documents
  remove: [phone]            # anonymize: these fields are removed
  replace:                   # anonymize: these fields get a fixed value
    buyer_name: "Erased customer"
  pull:                      # in EVERY document of the collection: remove the user from these arrays
    members: email           # what the array holds: email | id
  unset:                     # in EVERY document of the collection: clear these fields where they hold the user
    paid_by: email           # email | id
```

- `action` applies to the documents the user owns (`_meta.owner` is their id). `delete` removes them; `anonymize` removes the `remove` fields, sets the `replace` fields to their fixed value and clears the owner.
- `pull` and `unset` reach **every** document of the collection, whoever owns it. Each field says what it holds, `email` (the user's address, in the lower-case form `backd` stores) or `id`.
- Only realms with `auth: enabled` have users, so the file is an error in a realm with `auth: disabled`.
- `backd` checks the policy against `schema.json` at startup, so an erase leaves every document valid: named fields must exist; a required field can't be removed or unset (make it optional, or `replace` it); `replace` values must satisfy the schema and can't be in a unique index; `pull` fields must be arrays of strings without `minItems`; `unset` fields must be strings.
- `backd provision` creates the indexes an erase searches by, so large collections aren't scanned: `_meta.owner` when the policy has an `action`, and one on each `pull` and `unset` field (they are checked in `verify` mode like the ones in `indexes.json`). Adding a policy to an existing collection builds them at the next provision.
- The file is part of the [config fingerprint](../../operations/deploying/). Applying a policy is an erase, which is an administrator's action.

### Soft delete

By default `DELETE` removes a document for good. A collection can keep deleted documents recoverable instead:

```yaml
soft_delete:
  retention: 30d     # optional: remove them for good this long after the delete
```

`soft_delete: true` is the same without a retention. What changes in a collection that soft-deletes:

- **`DELETE` marks the document** (`_meta.deleted_at`, `deleted_by` and, with a retention, `purge_at`, at the next version) instead of removing it. No read, list, count, write or batch sees it; a unique value it held is free for a new document at once.
- **The trash is a separate view**: `?deleted=only|include` on list and get, `POST /{id}/restore`, and `DELETE /{id}?purge=true`, each under its own rule (`restore` and `purge` in the [`rules:`](../../auth/rules/#operations), denied unless declared). See [Soft delete](../../api/documents/#soft-delete).
- **A retention is MongoDB's TTL index**: `backd provision` creates it on `_meta.purge_at` and MongoDB removes the documents in the background, within about a minute of that time. Without a retention a deleted document stays until it is purged by hand.
- **Unique indexes** from `indexes.json` get `_meta.deleted_at` as their last key, so live documents stay unique among themselves, a deleted one never clashes with anything, and documents that existed before count as live. Turning `soft_delete` on for a collection that already has a unique index builds the new index next to the old one, and `backd provision` warns about the old one: it also covers deleted documents, so **drop it** once the new one exists (`db.<collection>.dropIndex("<name>")`) or a deleted document's values stay taken. `backd` never drops an index itself.
- **Erasing a user** still deletes, anonymizes and clears documents that are in the trash: it works on everything the user owns.
- It doesn't need users: a realm with `auth: disabled` can soft-delete, though it has no rules to restrict the trash.

### Custom ids

By default the server generates a document's id. A collection can instead declare what its ids look like, for ids that mean something: an invoice number, an external reference, or the user's own id for one document per user.

```yaml
custom_id:
  pattern: '^INV-[0-9]{4}-[0-9]{4,6}$'
  duplicate_limit: 20      # optional: duplicate creates per caller per minute (1 to 1000, default 20)
```

```yaml
custom_id: user            # the id must be the creating user's id (the realm needs users)
```

- **The pattern is anchored** automatically (the whole id must match), uses Go's RE2 syntax (no lookaheads or backreferences, so it can't hang a request) and has at most 512 characters. It is checked when backd starts.
- **A safety envelope always applies**, whatever the pattern: 1 to 128 characters, only letters, digits and `-` `.` `_` `~` `:` `@`, not starting with `_` or `.` (backd's own path segments such as `_files` start with `_`), and no `..`.
- **`backd data check`** reports stored documents whose id doesn't match the pattern (path `id`, never the value), for example after adding `custom_id` to a collection that already holds generated ids. Existing documents are never changed.
- Collections without `custom_id` are unchanged.

- **Creating** sends the id in the body of the `POST`; an id that exists is `409 id_taken`, and the `create` rule runs first and can read it as `data.id`. See [Custom ids](../../api/documents/#custom-ids).
- **Ids never change.** To change one, create a new document and delete the old one. [Custom ids](../custom-ids/) covers when to use them, probing, personal data and changing a collection that already has documents.

### Organizations

```yaml
organization: required       # required | optional | none (default)
on_org_delete:
  action: delete             # delete | keep
on_owner_delete:
  action: delete
  in_org: keep               # keep (default) | apply
```

With `organization: required`, every document is created inside an organization of the realm; with `optional`, some are. The collection has to say what happens to its documents when an organization is deleted: `delete` removes them, `keep` leaves them (readable only by realm administrators). `on_owner_delete.in_org` says what an erased user's documents do inside an organization: `keep` (the default) leaves them with the organization, owner cleared, `apply` treats them like documents outside organizations.

- The realm needs an [`organizations:`](../realm/#organizations) section, and `auth: enabled`.
- A collection with an organization can't also use `custom_id: user`: it would allow one document per user across every organization.
- `on_org_delete` is required, and only valid next to `organization`.
- When a user is [erased](../../auth/erasure/), their documents in an organization **stay**, with the owner cleared; with `in_org: apply` the erase's `action` applies to them as it does outside organizations (`in_org` needs an `action`).
- `_meta.org` is a system field: it can be queried, sorted and used in `indexes.json` (`[_meta.org, slug]` makes `slug` unique per organization). `backd provision` adds the index `(_meta.org, _meta.created_at, id)`, which serves a list of one organization in the default order.
- [Access rules](../../auth/rules/#organizations) read it with `inOrg`, `hasOrgRole` and `orgClaims`, applied inside the query.

#### Teams

A collection with an organization can also say its documents belong to a **team** of it:

```yaml
organization: required
on_org_delete:
  action: delete
team: required               # required | optional | none (default); needs `organization`
on_team_delete:
  action: keep               # keep | delete
```

`_meta.team` is server-owned like `_meta.org`: set at creation with `?team=<id>` (next to `?org=`), by a member of the team or a holder of a [team role](../../organizations/team-roles/) in it, and fixed afterwards (administrators move documents with the [transfer](../../api/documents/#fixing-data-as-an-administrator)). `on_team_delete` is required next to `team`: `delete` removes the team's documents when it is deleted (a job, in batches), `keep` leaves them with the old team id, readable through organization-level rules. [Access rules](../../auth/rules/#organizations) read it with `inTeam` and `hasTeamRole`.

### Public caching

Document responses are `private, no-cache` by default: browsers keep them but revalidate with `If-None-Match`, and shared caches never store them. A collection whose content anyone may read can let a CDN serve it:

```yaml
cache:
  public: true
  max_age: 60        # seconds, 1 to 3600
rules:
  read: document.published == true
```

- **Only requests without credentials** get `Cache-Control: public, max-age=60` (plus the `ETag`): no `Authorization` header and no cookie. The same documents read by a signed-in user, or with an API key, stay `private, no-cache`. `Vary` keeps the two apart.
- **Never public:** the trash (`?deleted=`), and responses with `?file_links=true`.
- **`backd` refuses to start** when `public: true` is declared without a `max_age` between 1 and 3600, in a collection with no `read` rule, with one anonymous callers can never satisfy (`user != nil`, `hasRole(…)`), or one that depends on `now`.

{{< hint style="warning" >}}
A document that stops being readable by anonymous callers (unpublished, deleted) can still be served by a CDN until its `max_age` runs out. Use a short `max_age` for content you may need to withdraw.
{{< /hint >}}

### Personal data scope

A user's right to a copy of their data is answered from the same model as erasure: **the documents whose `_meta.owner` is the user** (whatever their `on_owner_delete` action), the documents where the user's id or email appears in a `pull` field, and the files those documents hold. `personal_data:` overrides that per collection:

```yaml
personal_data:
  export: include                 # include (default) | exclude
  fields: [title, notes]          # optional: only these fields, plus id and _meta
  reason: "scores are internal"   # required to exclude, and when fields leave data out
```

- **`export: exclude`** needs a `reason`: what exempts the data (fraud scores, legal holds). Keep it true: a request can ask why something was not given.
- **`fields`** narrows the export to those fields, for collections that mix the user's data with other people's (comments, reviews). Every field must exist in `schema.json` and `reason` is required.
- **`backd` refuses to start** on unknown fields, a missing reason, or `personal_data` in a realm with `auth: disabled`.
- **Read and `reveal` rules do not limit the scope**: an access request covers data *about* the person, whatever the app shows them.

See what an export would take with `backd compliance scope --realm <r>` (or `GET /_admin/compliance/scope`): per collection what is included, which fields, and every exclusion with its reason, plus the system data an export adds (account, identities, sessions, devices, MFA status, consents, audit records about the user, invocations they made) and what it never holds (password hashes, tokens, secrets, function inputs, outputs and logs).

Building and delivering the export package is a later step: today the scope is declared, checked and reported. Backd provides the mechanisms; legal bases, deadlines and texts are yours.

### Files and erasure

`on_owner_delete` covers file fields: `delete` queues every file of the user's documents, and `anonymize` with a file field in `remove` queues that field's files. Without a policy the files stay. See [Keeping storage tidy](../../files/maintenance/#what-queues-an-object).

### File fields

`files:` declares the fields of a document that carry files, kept in the realm's [storage](../../files/storage/): `max_size`, `types`, `multiple`, `max_files`, `download`, [`versions`](../../files/file-fields/#image-versions) and more. See [File fields](../../files/file-fields/). Don't declare them in `schema.json`: backd adds their schema.

## App configuration

A realm can declare one configuration for its client apps (feature flags, maintenance switches, limits) in `<realm>/app-config/`, with the same JSON Schema validation as collections and read rules in the same expression language. It needs a realm with auth enabled.

```json
{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "maintenance": {
      "type": "object", "additionalProperties": false,
      "properties": {
        "enabled": { "type": "boolean", "default": false },
        "message": { "type": "string", "maxLength": 500, "default": "" }
      }
    },
    "features": {
      "type": "object", "additionalProperties": false,
      "properties": { "export": { "type": "object", "additionalProperties": false,
        "properties": { "enabled": { "type": "boolean", "default": true } } } }
    }
  }
}
```

```yaml
# rules.yaml
read:
  maintenance: "true"                  # anyone, without a session
  features: user != nil                # signed-in users
  features.export: hasRole(user, "beta")
```

- **`schema.json`:** keys are paths in dot notation (`features.export.enabled`), and a subset is a branch (`features`). Every object sets `additionalProperties: false` and declares its properties; every value has a `default`, or allows `null` among its types, so clients always get a complete configuration. `$ref`, `allOf`, `anyOf`, `oneOf` and other composition keywords are not supported. The defaults take at most 64 KiB, and none may be a `secret:` reference.
- **`rules.yaml`:** `read:` maps a path to a rule that sees `user` and `now`, with `hasRole`, `hasConsent` and the realm's roles and consents; there is no document. A path's effective rule is its own ANDed with every ancestor's, so deeper paths can only narrow. A path with no rule on it or on an ancestor can't be read by clients.
- **Startup** fails on an uncompilable schema or rule, an object missing `additionalProperties: false`, a value without a default, a rules path the schema doesn't declare, a default that doesn't satisfy the schema, a `secret:` reference or defaults over the size cap. Both files are part of the config fingerprint.

The [app configuration guide](../app-config/) covers reading it with `GET /v1/{realm}/_config`, changing values, functions and rules.

Configuration is readable by whoever its rules allow, and sits in the browser's cache: never put a secret in it.

## Errors

Any invalid config stops startup with a non-zero exit. Every error is reported at once, each naming the offending file or directory. Examples of invalid config:

- a missing `CONFIG_DIR`;
- a realm without `realm.yaml`, or an invalid `realm.yaml` (see [Realm settings](../realm/#validation));
- an invalid name;
- a collection directory without `schema.json`;
- invalid JSON or an invalid schema;
- a schema that declares a system field;
- an invalid `indexes.json`;
- an invalid [`rules:` section](../../auth/rules/#checks-at-startup), or a leftover `rules.yaml` (rules live in `collection.yaml` now);
- an invalid [app configuration](#app-configuration);
- an invalid [`collection.yaml`](#collectionyaml) (an unknown field, a required field removed, a value that breaks the schema).
