# Design: custom ids

- **Issues:** features 25.1–25.6 (milestone `25-custom-ids`)
- **Status:** decided
- **Changes an earlier decision:** "the server always generates the ids". It still does by default; collections can now opt in to client ids. `decisions.md` records the change (25.1).
- **Related designs:** [reference-expansion.md](./reference-expansion.md) (`key: id` default), [field-encryption.md](./field-encryption.md) (values bound to the document id), [export-import.md](./export-import.md), [realtime.md](./realtime.md), [types-openapi.md](./types-openapi.md)

## 1. The idea

Some collections need ids that mean something: an invoice number, an external reference, or **the user's own id** for one-per-user documents (profiles, settings). A collection can declare `custom_id` in `collection.yaml`; then every create must provide an id matching its pattern. Collections without it keep server-generated xids, unchanged.

- **Safe by construction:** a fixed safety envelope keeps ids URL-safe and away from backd's reserved segments, whatever the pattern says.
- **No overwrites:** creating with an id never replaces an existing document.
- **Probing is limited:** the `create` rule runs before any existence check, duplicates are rate-limited, and the docs recommend creating meaningful ids through functions.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Declaring | `custom_id: { pattern, duplicate_limit }` or `custom_id: user` in `collection.yaml` |
| Required | collections with `custom_id` require an `id` on every create; no xid fallback |
| Creating | `POST` with `id` in the body; `PUT` and `PATCH` stay update-only |
| Order of checks | id validation, then the `create` rule (`403`), then insert (`409 id_taken` on duplicates) |
| Probing | duplicate attempts rate-limited per caller (configurable); docs recommend functions for meaningful ids |
| Envelope | 1–128 URL-safe characters, not starting with `_` or `.`, no `..`; pattern anchored, RE2, at most 512 characters |
| `user` | the id must equal the creating user's id |
| Personal data | documented implications only |
| Ordering | lists without `order_by` sort by `_meta.created_at`, then id |

## 3. Declaring (issue 25.1)

```yaml
# collection.yaml of "invoices"
custom_id:
  pattern: '^INV-[0-9]{4}-[0-9]{4,6}$'
  duplicate_limit: 20          # duplicate attempts per caller per minute (default 20)
```

```yaml
# collection.yaml of "profiles"
custom_id: user                # id must equal the creating user's id
```

- **The safety envelope** always applies, in addition to the pattern:
  - length 1–128;
  - characters limited to letters, digits and `-` `.` `_` `~` `:` `@`;
  - must not start with `_` (backd's reserved segments such as `_files`, `_aggregate`, `_transfer`) or `.`;
  - must not contain `..`.
- **The pattern:**
  - anchored automatically (`^(?:…)$`), so partial matches never pass;
  - compiled with Go's RE2 engine, which runs in linear time, so a pattern can't hang a request;
  - at most 512 characters.
- **Startup checks:**
  - the pattern compiles;
  - it isn't empty;
  - backd warns when a sample of strings shows the pattern accepts ids the envelope refuses (a hint the pattern is wider than intended);
  - `duplicate_limit` is between 1 and 1,000.
- **`custom_id: user`** needs a realm with users. The collection's `create` rule should still check whatever else matters; backd enforces `id == user.id` itself.
- **`backd check`** reports existing documents whose ids don't match the pattern, for example after adding `custom_id` to a collection that already holds xids.
- **`decisions.md`** records the change to the "server always generates ids" decision and why.

## 4. Creating (issue 25.2)

```
POST /v1/{realm}/billing/invoices
{ "id": "INV-2026-0042", "customer": "…", "total": 120 }
→ 201 Created
→ 409 id_taken          (exists)
```

- **Missing or wrong ids:**
  - `id` missing in a `custom_id` collection: `400 id_required`;
  - not matching the envelope or pattern: `400 invalid_id`, naming which check failed but never echoing other documents;
  - `custom_id: user` with an id that isn't the caller's: `400 invalid_id`;
  - `id` sent to a collection **without** `custom_id`: `400 id_not_allowed`.
- **Order of checks:**
  1. id validation;
  2. the `create` rule, which sees `doc.id`, so `403` for callers not allowed to create;
  3. insert, with `409 id_taken` on a duplicate.

  Callers who can't create in the collection never learn whether an id exists.
- **Duplicate rate limit:** after `duplicate_limit` duplicates in a minute, the caller gets `429` with `Retry-After`. Metrics count duplicates per collection.
- **`PUT` and `PATCH`** stay update-only (`404` when missing). No upsert.
- **Everywhere creates happen:**
  - `_batch` creates and `ctx.db.create({ id, … })` take the same `id`;
  - `ctx.admin.db` creates follow the pattern and envelope too, but no rule.
- **`Idempotency-Key`** (7.16) keeps giving retried requests their original answer.
- **Ids never change.** Changing one means creating a new document and deleting the old one. The docs show it, including references.

## 5. Ordering and other id assumptions (issue 25.3)

xids are time-ordered, and custom ids aren't. The audit makes sure nothing silently relied on that.

- **Lists without `order_by`:** sort by `_meta.created_at`, then id. A breaking change, which is allowed. The query translator, docs and OpenAPI change accordingly.
- **Cursor pagination** keeps id as the final tie-breaker, which works for any string.
- **Reserved segments:** routing tests with ids next to every reserved segment (`/_files`, `/_aggregate`, `/_transfer`, `/_expand`…), proving custom ids can't collide.
- **Features checked with custom ids, by tests:**
  - expansion (`key: id` default; `custom_id: user` collections expand from `_meta.owner` without `key:`);
  - encrypted values (bound to the document id);
  - realtime events and resume;
  - export and import (ids preserved);
  - files (stored by file id);
  - `ETag`s and conditional GET;
  - the pushdown property tests.
- **Export order:** exports sort by `_meta.created_at`, then id.

## 6. Client, types and OpenAPI (issue 25.4)

- **JS client:** `collection("invoices").create({ id: "INV-2026-0042", … })`, with typed errors (`id_required`, `invalid_id`, `id_not_allowed`, `id_taken`).
- **Types (23):** collections with `custom_id` type `id` as required on create. `custom_id: user` documents the constraint in the type's comment.
- **OpenAPI:** the create body requires `id` with the pattern (anchored, as JSON Schema `pattern`), and path parameters carry the pattern.

## 7. Shelf (issue 25.5)

- **Profiles:** Shelf's `profiles` collection becomes `custom_id: user`, so each user's profile lives at `/v1/{realm}/main/profiles/<user id>`.
- **Simpler expansion:** the `author` reference expands from `_meta.owner` with the default `key: id`, replacing the `key: _meta.owner` recipe from 15.5, and `profiles` no longer needs its unique index on `_meta.owner`.
- **Tests:** Shelf code and tests are updated, and earlier chapters still pass.

## 8. Documentation (issue 25.6)

- **When to use custom ids:** one-per-user documents (`custom_id: user`), external references, human-readable numbers. When not to: anything that might change, or that contains personal data.
- **Probing:**
  - what a client learns from `409`;
  - the order of checks;
  - the duplicate limit;
  - **for meaningful ids, close direct creation (`create: false`) and create through a function** that decides or checks the id;
  - direct creation is fine when rules tie the id to the caller.
- **Personal data in ids:** ids can't change or be anonymized; they appear in URLs, logs, references, exports and backups; erasure can't remove personal data held in an id, so such documents need `on_owner_delete: delete` to disappear. The advice is to avoid personal data in ids.
- **Changing an id:** create, then move references, then delete.
- **Adding or removing `custom_id`** on existing collections, and what `backd check` reports.
- **The profile-per-user recipe.**

## 9. Issues

| id | Issue | Depends on |
|---|---|---|
| 25.1 | Declaring custom ids | — |
| 25.2 | Creating with custom ids | 25.1 |
| 25.3 | Audit of id assumptions | 25.2 |
| 25.4 | Custom ids in the client, types and OpenAPI | 25.2, 23.2 |
| 25.5 | Shelf profiles keyed by user id | 25.2, 15.5 |
| 25.6 | Custom ids documentation | 25.3, 25.4, 25.5 |

**When to build:** right after types and OpenAPI (23), before organizations. 25.5 waits for 15.5.

## 10. Alternatives considered

- **Upsert through `PUT`:** rejected because it mixes create and update rules and reopens overwrite races.
- **Functions only:** rejected because one-per-user documents need plain client creates.
- **An optional id with an xid fallback:** rejected; ids in a collection follow one pattern.
- **Hiding existence by answering `403` for unreadable duplicates:** replaced by `409` with the `create` rule first, a rate limit, and functions for meaningful ids.
- **Built-in formats only:** rejected; patterns are needed for real numbering schemes.
- **A `personal` flag forcing deletion on erasure:** rejected as overkill; documented instead.

## 11. Follow-ups

- Upsert as an explicit, separate option if data synchronization needs it.
- Built-in pattern shortcuts (`uuid`, `ulid`, `slug`) if patterns prove error-prone.
