---
title: "Architecture"
description: "Packages, the query pipeline, storage mapping and conditional writes."
icon: "tree-structure"
weight: 100
toc: true
---

`backd` is a single stateless Go service. Apart from the registry built at startup, all state lives in MongoDB, so several identical instances can run side by side.

```
CONFIG_DIR ──load──▶ registry ──▶ provisioner ──▶ MongoDB
                        │                          ▲
HTTP ──▶ handlers ──────┴──▶ storage.Repository ───┘
```

## Layers

| Package | Role |
|---|---|
| `internal/settings` | Environment variables → `Settings` |
| `internal/registry` | Immutable realm → database → collection tree, with realm settings (`realm.yaml`), compiled schemas and field index |
| `internal/templates` | Embedded starter files written by `backd template` |
| `internal/rules` | Compiles the `rules:` of `collection.yaml`: expr-lang rules and runs the static checks (guards, roles, fields, `now`, read rules as filters) |
| `internal/auth` | Storage-neutral identity model (users, identities, sessions), password policy, argon2id hashing with a concurrency cap, sign-up, login and session handling; persistence behind `auth.Store` (in-memory fake in `internal/auth/authtest`) |
| `internal/query` | Parses the list query language into `storage.Condition`s, checking every field and value against the schema |
| `internal/storage` | Backend-neutral `Repository` interface, `Document`/`Query`/`Page`/`Condition`/`Filter` types, `Fetch` (single reads under a read rule), and the errors `ErrNotFound`, `ErrVersionMismatch`, `ErrConflict` and `ErrUnavailable` |
| `internal/httpapi` | Router, middleware, error envelope, document, `/_auth` and `/_admin` handlers, CORS |
| `internal/jsonnum` | Normalizes JSON numbers to int64 or float64 |
| `internal/mongodb` | Everything MongoDB-specific: connection, validator translation, provisioner (including realm system databases), `Repository` and `auth.Store` implementations |

The packages above are the core. The features built on it have packages of their own, each driver-neutral where it can be:

| Package | Role |
|---|---|
| `internal/server`, `internal/servercli` | The server roles (API, worker, provisioning, executor and egress bootstrap), their startup checks, and the `backd-server` command tree |
| `internal/cli`, `internal/cmdutil`, `internal/apiheaders`, `internal/adminclient` | The `backd` command tree, the helpers both command trees share, the header names the CLI and the server both read, and the `/_admin` side of the Go client |
| `internal/functions`, `internal/executor`, `internal/egress`, `internal/cron` | Building function bundles (and schema migrations), the Deno supervisor that runs each call and migration batch, the egress proxy, and cron expressions |
| `internal/changefeed`, `internal/triggers`, `internal/realtime` | The change stream reader, the trigger dispatcher with its durable position, and server-sent subscriptions |
| `internal/email`, `internal/push`, `internal/pushsend` | Email kinds and templates, push messages and templates, and the built-in FCM, APNs and Web Push senders |
| `internal/oauth` | Google, Microsoft and Apple: authorize URLs, code exchange, ID-token verification |
| `internal/authcore`, `internal/fieldcrypt` | API key scopes, the secrets cipher and field key encoding; per-field encryption before data reaches the database |
| `internal/imaging`, `internal/exportpkg`, `internal/exportreadme` | Image versions, the export package format, and the README of a personal-data export |
| `internal/appconfig`, `internal/ratelimit`, `internal/metrics` | App configuration, the in-memory request limiter, Prometheus instrumentation |
| `internal/typegen`, `internal/openapigen`, `internal/rulestest`, `internal/devreport` | Generated types, the per-realm OpenAPI document, `backd rules test`, and what dev mode reports when a config doesn't load |
| `internal/adminui`, `internal/buildinfo`, `internal/dbnames`, `internal/docutil`, `internal/strutil` | The embedded admin interface and small shared helpers |

No MongoDB driver types cross the `storage` interface. Another document store (for example Amazon DocumentDB) can be added as a new implementation without touching handlers or validation.

## Query pipeline

Client queries never reach the database as-is:

1. `internal/query` decodes `where` and accepts only the operators backd defines.
2. It checks each field against the collection's field index (plus `id` and the `_meta` fields: timestamps, `version` and the ownership fields) and type-checks each value against the schema. Read [access rules](../../build/auth/rules/#read-rules-are-database-filters) are added as a separate `storage.Filter` tree.
3. It produces a backend-neutral `storage.Filter` tree: `storage.Condition` leaves (field path, operator and normalized value) combined with `And` and `Or` for the object's keys and its `$or` / `$and` groups.
4. The repository translates the tree, and each operator, into its own query dialect, and ANDs it with the read rule's filter.

The field index maps dot paths to their JSON Schema types. It includes properties of objects inside arrays under the array's path (for example `lines.sku`), matching MongoDB's dot notation. For array fields it also records the item types, so a scalar can be matched against array elements.

## Stored documents

The repository maps between the API shape and the stored shape:

| API | MongoDB |
|---|---|
| `id` (string, [xid](https://github.com/rs/xid)) | `_id` (string) |
| `_meta.created_at` / `_meta.updated_at` | BSON dates |
| `_meta.version` | 64-bit integer |
| `_meta.schema_version` | integer, only in [versioned collections](../../build/configuration/config-dir/#versioned-schemas); a document without it is version 1 |
| `_meta.owner`, `_meta.created_by`, `_meta.updated_by` | strings (`owner` may be `null`), in realms with an `auth` section |
| user fields | stored with keys sorted (recursively), after `_id` and `_meta` |

Sorting keys makes stored documents deterministic. It also means that equality on a sub-document doesn't depend on the key order the client sent.

## Conditional writes

`Repository.Replace` and `Delete` take the version the handler read. The MongoDB implementation adds `_meta.version` to the write filter (`null` for documents stored before versioning). When nothing matches, it checks whether the document still exists, and reports `ErrNotFound` or `ErrVersionMismatch`. PUT and PATCH are implemented in the handler as get → (merge) → validate → rules → conditional replace, retried with backoff up to 3 times; DELETE under access rules is get → rules → conditional delete, retried the same way.

Driver timeouts and network errors are reported as `storage.ErrUnavailable`, which the HTTP layer maps to `503`.
