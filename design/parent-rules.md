# Design: rules that follow a reference (`parent()`)

- **Issues:** #878–#881 (findings DF.1 and DF.11 of the tutorial)
- **Status:** decided
- **Related designs:** [reference-expansion.md](./reference-expansion.md) (`references:`), [triggers-and-realtime.md](./triggers-and-realtime.md) (streams), [organizations.md](./organizations.md) (the other helpers that are both applied inside a query and evaluated in memory)

## 1. The problem

A rule sees only the document it is about. A child collection can't inherit access from its parent: `items` can't say "readable by the members of `lists`". The options were to copy the parent's members onto every child (each membership change rewrites every child) or to put every operation behind a function, which also rules out realtime on the children: a stream applies the subscriber's read rule, and the children have none.

## 2. What a rule can write

```yaml
# items/collection.yaml
references:
  list: {field: list_id, collection: lists}
rules:
  read:   user != nil && user.id in parent(document.list_id).members
  create: user != nil && user.id in parent(data.list_id).members
  update: >
    user != nil && user.id in parent(document.list_id).members
    && user.id in parent(data.list_id).members
  delete: user != nil && user.id in parent(document.list_id).members
```

- `parent(x)` takes the field that holds a declared single-valued reference, as `document.field` or `data.field`, and stands for the document that reference points to. It must be followed by a field of the parent: `parent(document.list_id).members`.
- One level, read only: no `parent(parent(…))`, and only the parent's fields.
- Not in triggers, invoke, config or reveal rules; not with `many: true` references.

## 3. Semantics: a term is "exists a parent where"

Every term that reads from a parent (a comparison, `in`, a boolean field) is a *unit*. A unit is true when the parent exists, is not deleted, and the comparison is true of it. Units combine with `&&`, `||` and `!` like any other term, and negation applies to the whole unit, so `!(user.id in parent(document.list_id).members)` is true for an item whose list is gone. A missing, deleted or unreadable-by-key parent never makes a unit true.

Because a document has exactly one parent, several units over the same reference in one rule are equivalent to one unit over their combination, except under negation; the per-term definition above is what the code does and what the tests pin down.

Inside a unit the other side of a comparison can use `user`, constants, `hasRole`, `appConfig` and the like, but not `document`, `data`, `before` or `changed()`: the comparison is made on the parent, and the other side is decided per request. Organization helpers are not allowed in a unit. Encrypted fields of the parent are not allowed. Unit predicates follow the same rules as read rules (a database filter on the parent's own fields: scalar comparisons, `in`, membership of an array field), checked against the parent's `schema.json` once the realm is loaded.

The parent's own read rule is **not** applied: the condition is written explicitly in the child's rule, and only a boolean comes out of it. Whoever writes `user.id in parent(document.list_id).members` decides that members of the list may see its items.

## 4. How it is evaluated

The rules package turns each unit into a hidden call `__parent(idx, key)`, and keeps the unit's predicate (the term with `parent(x)` read as `document`) to build a filter on the parent collection with the same code that builds read filters.

- **In memory** (create, update, delete, and reads checked document by document): fetch the parent by the key in the field, match the predicate's filter against it with `storage.Match`. One `Get` per unit, memoized per request by the resolver.
- **As a filter** (read, restore: pushed into the query): the predicate's filter is resolved to the keys of the matching, live parents (`Parents.Keys`), and the term becomes `list_id in keys`. This is a semi-join worked out at the start of the request, so what the rest of the system sees is an ordinary condition: `storage.Match`, realtime, `backd rules test` and the MongoDB translation need no new operator. Beyond the instance's cap on keys the request fails closed instead of showing fewer documents.

Both paths use one predicate and one `Match`, so they agree by construction; `TestParentRulesAgree` (and the MongoDB differential test) still check it against every caller and a set of documents with missing, deleted and null parents.

`$lookup` was rejected: it breaks the repository abstraction, and `Match`, realtime and the rules test runner could not evaluate it.

## 5. Cost

- A read request costs one more indexed query on the parent collection (the members must be indexed for that to be cheap: `indexes.json` with a multikey index on `members`). A write costs one `Get` per unit.
- The number of keys is capped (2,000, a constant of the HTTP layer); a user who is a member of more lists than that gets an error (answered as a denial), not a partial list.
- Realtime: see §6.

## 6. Realtime

The subscription's filter is resolved once, when it is made, to `list_id in keys`, which `storage.Match` evaluates per change like any other filter. It depends on documents of another collection, so it must be rebuilt when they change, like it is for sessions and organization memberships:

- `realtime.Access.Parents` lists the parent collections the subscribed read rules follow; the hub keeps a watcher index by collection, and `Hub.Change` makes the watching streams reload their access (coalesced, at most one per stream every 250 ms, as for organizations).
- A reload re-resolves the filter. If the set of keys changed, the subscription gets `reset` (`access_changed`); if not, nothing is sent.
- The parent collection must have `realtime: true` (checked at startup) so the change feed delivers its changes.
- A change in a child between two reloads is judged with the keys in force at that time; the reload that follows a membership change resets the client, which re-reads. Nothing is shown that the old keys did not allow.
- The collection's visibility key includes where the references the read rule follows point, so changing a reference ends the subscriptions made under the old configuration.

Any change in the parent collection triggers a reload of its watchers, not only changes of the fields the rule reads; parents are expected to change at the pace of memberships.

## 7. Decisions

| Topic | Decision |
|---|---|
| Syntax | `parent(document.field)` / `parent(data.field)` on a declared reference |
| Depth | one level, read only |
| Missing or deleted parent | the unit is false |
| Parent's read rule | not applied |
| Pushdown | semi-join resolved per request, not `$lookup` |
| Negation | applies to a whole unit |
| Encrypted parent fields, `now`, organization helpers in a unit | refused |
| Triggers | refused |
