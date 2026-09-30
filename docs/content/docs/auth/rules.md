---
title: "Access rules (rules.yaml)"
description: "Decide per collection which callers may read, create, update and delete documents."
icon: "policy"
weight: 540
toc: true
---

Each collection of a realm with `auth: enabled` can have a `rules.yaml` next to its `schema.json`. Rules decide, per operation, whether a caller may act on a **whole document**. There are no per-field permissions: data that needs different visibility belongs in a separate collection with its own rules.

```
$CONFIG_DIR/<realm>/<database>/<collection>/
    schema.json
    indexes.json     # optional
    rules.yaml       # optional
```

{{< hint warning >}}
**API keys are not subject to rules.** A service holding a key can read and change everything in its realm. Rules protect users from each other, not the realm from its own keys: see [API keys](../api-keys/).
{{< /hint >}}

## Example

```yaml
# Anyone may read published posts; authors also see their drafts.
read: >
  document.published == true
  || (user != nil && document._meta.owner == user.id)

# Signed-in users with a verified email may write posts.
create: user != nil && user.email_verified

# Authors may edit their own posts, but not move them to another status.
update: >
  user != nil && document._meta.owner == user.id
  && !('status' in changed())

# Only admins may delete.
delete: hasRole(user, 'admin')
```

## Operations

| Key | Controls |
|---|---|
| `read` | Listing documents and fetching one |
| `create` | `POST` |
| `update` | `PUT` and `PATCH` |
| `delete` | `DELETE` |
| `write` | Shorthand used for `create`, `update` and `delete` when they aren't set |

Anything not allowed is denied: an operation without a rule (and no `write` to fall back on) is forbidden for users and anonymous callers. A collection without `rules.yaml` is only reachable with API keys.

Each value is an [expr](https://expr-lang.org/docs/language-definition) expression that must be true to allow the operation.

{{< hint note >}}
YAML reads a value starting with `'`, `!`, `[` or `{` specially, so quote such rules, or use a block (`>`) as in the example above. A rule that quietly parses as something else is one of the few ways to get a rule you didn't write; `backd` refuses to start on most of them, but not all.
{{< /hint >}}

## What rules can use

| Name | Available on | Meaning |
|---|---|---|
| `user` | all | The signed-in user: `user.id`, `user.email`, `user.email_verified`, `user.roles`. `nil` for anonymous callers |
| `document` | read, update, delete | The stored document, including `document.id` and `document._meta.*` |
| `data` | create, update | The document being written: the request body on create, the complete result on update |
| `now` | all | The current time, only for comparing with `document._meta.created_at` or `document._meta.updated_at` |
| `hasRole(user, 'a', 'b', …)` | all | True if the user has any of the roles; false for anonymous callers |
| `changed()` | update | The top-level fields whose value differs between `document` and `data` |

Rules have no access to the request itself (headers, address) and can't look up other documents. For shared documents, store member ids in an array and check `user.id in document.members`.

### Anonymous callers

A request without credentials is anonymous: `user` is `nil`. So `read: "true"` makes a collection public, and `user != nil` means "any signed-in user".

{{< hint style="tip" title="Already done for you" >}}
`backd` checks every `rules.yaml` when it starts and refuses to run, naming the file, on an unknown field or role, an unguarded `user` field, or a `read` rule that can't become a database filter. A typo can't silently open or close a collection: fix it and restart.
{{< /hint >}}

Every use of a `user` field must be guarded, so a rule can't misbehave for anonymous callers:

```yaml
read: user != nil && document._meta.owner == user.id    # ok
read: document._meta.owner == user.id                   # startup error: add `user != nil &&`
read: user == nil || user.email_verified                # ok: guarded by `user == nil ||`
read: document.published || hasRole(user, 'editor')     # ok: hasRole needs no guard
```

Negations are literal: `!hasRole(user, 'banned')` is true for anonymous callers, because they have no roles.

### Roles

Role names must be declared in the realm's [`realm.yaml`](../../configuration/realm/#roles). Using an undeclared role, with `hasRole` or with `'x' in user.roles`, stops startup.

### Read rules are database filters

`read` rules also decide which documents a list returns, so they are applied inside the database query: pages, `has_more` and `count` only ever include readable documents. That's why a `read` rule must be made of:

- comparisons between a document field and a value: `==`, `!=`, `<`, `<=`, `>`, `>=`, with a field on one side;
- `document.field in [...]` and `value in document.arrayField`;
- boolean fields on their own (`document.published`);
- `&&`, `||`, `!` and parentheses.

Compared fields must be declared in `schema.json` with a type that isn't `array`, and not sit inside an array. The database compares arrays element by element (`{tags: "x"}` matches any array containing `"x"`), which differs from what the rule says, so `document.tags == 'x'` stops startup. Write `'x' in document.tags` instead: it means the same in the rule and in the database.

The value side can be anything that doesn't depend on the document: literals, `user` fields, `hasRole(...)`, `now` with arithmetic such as `now - duration('24h')`. It is computed once per request. Rules comparing two document fields, or calling functions on document fields, stop startup with an explanation.

#### Missing and null fields

In a `read` rule, a field that is missing or `null`:

- never equals, exceeds or is in anything: `document.n > 3`, `document.status == 'a'` and `document.status in ['a']` don't match it;
- matches `== nil`, `!=` and negations: `document.status == nil`, `document.status != 'a'`, `!(document.n > 3)` and `not document.published` all match it.

So `not document.published` includes documents without `published`. If that's not what you want, write `document.published == false`.

These are the database's semantics, and they apply to lists and to fetching one document alike. Tests compare the pushed-down filter with the rule itself on hundreds of documents, for every supported construct, to make sure the database never lists a document the rule doesn't allow.

`create`, `update` and `delete` rules are evaluated in memory, so they can use any expr feature, such as `len(data.title) < 100` or `all(changed(), # in ['title', 'body'])`.

Rules can check the submitted data against the caller, for example to make sure a post is signed with the writer's own email and never re-attributed:

```yaml
create: user != nil && data.author.email == user.email
update: user != nil && document._meta.owner == user.id && !('author' in changed())
```

`changed()` compares top-level fields, so a change anywhere inside `author` (for example `author.email`) counts as `author` changing.

## How rules are applied

In a realm with `auth: enabled`, every data request is decided like this:

| Caller | Rules |
|---|---|
| [API key](../api-keys/) | Not applied: full access |
| Signed-in user (session token) | Applied, with `user` set |
| API key [acting on behalf of a user](../api-keys/#acting-on-behalf-of-a-user) | Applied, with `user` set to that user |
| No credentials | Applied, with `user` set to `nil` |
| Invalid, expired or revoked credentials | Refused with `401`, never treated as anonymous |

For each operation:

- **List** (`GET /collection`): only documents the `read` rule allows are returned. The rule is combined with the client's `where`, so `limit`, `skip`, `has_more` and `count` only ever see readable documents.
- **Fetch** (`GET /collection/{id}`): the same `read` filter is applied, so a list and a fetch never disagree.
- **Create**: the `create` rule is checked with `data`, after schema validation.
- **Update** (`PUT`, `PATCH`): the document must be readable, then the `update` rule is checked with the stored `document` and the complete new `data`, after schema validation.
- **Delete**: the document must be readable, then the `delete` rule is checked. The delete only happens if the document hasn't changed since it was checked.

### Answers when a rule says no

| Situation | Status |
|---|---|
| The caller can't read the document (update, delete or fetch of a document the `read` rule hides) | `404 not_found`, exactly as if it didn't exist |
| The rule is false for every document (for example `read: user != nil` for an anonymous caller), or the operation isn't allowed | `401 unauthenticated` for anonymous callers, who may be allowed once signed in; `403 forbidden` for signed-in users |
| No rule for the operation, or no `rules.yaml` | Same as a false rule |

So a document's existence is only revealed to callers who could read it. A rule that fails while being evaluated (for example comparing a string with a number) denies.

{{< hint warning >}}
One exception is inherent to [unique indexes](../../configuration/config-dir/#indexesjson): a create or update that repeats a unique value answers `409 conflict`, even if the document holding that value is hidden. That tells the caller the value exists. Don't put unique indexes on values whose existence must stay secret.
{{< /hint >}}

At `LOG_LEVEL=debug`, every denial is logged (`"msg":"access denied"`) with the collection, the operation and the reason, never the document's contents.

## Checks at startup

`backd` refuses to start, naming the file, when a `rules.yaml`:

- isn't a mapping of the keys above to expressions, or has an unknown key;
- has a syntax error, or uses an unknown variable or `user` field;
- uses a `document` or `data` field that isn't in `schema.json`;
- uses `document` on create, `data` on read or delete, or `changed()` outside update (a `write` rule is checked for each operation it covers);
- uses an undeclared role, or a role name that isn't a string literal;
- uses a `user` field without a guard;
- uses `now` other than to compare with a `_meta` timestamp;
- has a `read` rule that can't be a database filter, or that compares an array field or a field without a declared type;
- exists in a realm with `auth: disabled`.
