---
title: "Architecture"
description: "Packages, the query pipeline, storage mapping and conditional writes."
icon: "account_tree"
weight: 910
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
| `internal/rules` | Loads `rules.yaml`: compiles expr-lang rules and runs the static checks (guards, roles, fields, `now`, read rules as filters) |
| `internal/auth` | Storage-neutral identity model (users, identities, sessions), password policy, argon2id hashing with a concurrency cap, sign-up, login and session handling; persistence behind `auth.Store` (in-memory fake in `internal/auth/authtest`) |
| `internal/query` | Parses the list query language into `storage.Condition`s, checking every field and value against the schema |
| `internal/storage` | Backend-neutral `Repository` interface, `Document`/`Query`/`Page`/`Condition`/`Filter` types, `Fetch` (single reads under a read rule), and the errors `ErrNotFound`, `ErrVersionMismatch`, `ErrConflict` and `ErrUnavailable` |
| `internal/httpapi` | Router, middleware, error envelope, document, `/_auth` and `/_admin` handlers, CORS |
| `internal/jsonnum` | Normalizes JSON numbers to int64 or float64 |
| `internal/mongodb` | Everything MongoDB-specific: connection, validator translation, provisioner (including realm system databases), `Repository` and `auth.Store` implementations |

No MongoDB driver types cross the `storage` interface. Another document store (for example Amazon DocumentDB) can be added as a new implementation without touching handlers or validation.

## Query pipeline

Client queries never reach the database as-is:

1. `internal/query` decodes `where` and accepts only the operators backd defines.
2. It checks each field against the collection's field index (plus `id` and the `_meta` fields: timestamps, `version` and the ownership fields) and type-checks each value against the schema. Read [access rules](../../auth/rules/#read-rules-are-database-filters) are added as a separate `storage.Filter` tree.
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
| `_meta.owner`, `_meta.created_by`, `_meta.updated_by` | strings (`owner` may be `null`), in realms with `auth: enabled` |
| user fields | stored with keys sorted (recursively), after `_id` and `_meta` |

Sorting keys makes stored documents deterministic. It also means that equality on a sub-document doesn't depend on the key order the client sent.

## Conditional writes

`Repository.Replace` and `Delete` take the version the handler read. The MongoDB implementation adds `_meta.version` to the write filter (`null` for documents stored before versioning). When nothing matches, it checks whether the document still exists, and reports `ErrNotFound` or `ErrVersionMismatch`. PUT and PATCH are implemented in the handler as get → (merge) → validate → rules → conditional replace, retried with backoff up to 3 times; DELETE under access rules is get → rules → conditional delete, retried the same way.

Driver timeouts and network errors are reported as `storage.ErrUnavailable`, which the HTTP layer maps to `503`.
