# Design: conditional GET

- **Issues:** features 20.1–20.5 (milestone `20-conditional-get`)
- **Status:** decided
- **Related designs:** [reference-expansion.md](./reference-expansion.md) (expanded responses are never `304`), [field-encryption.md](./field-encryption.md) (`reveal` and the representation tag, 16.4), [triggers-and-realtime.md](./triggers-and-realtime.md) (the push alternative to polling)

## 1. The idea

A client that already holds a response sends `If-None-Match` with its `ETag`. When nothing it would see has changed, backd answers `304 Not Modified` with no body. Polling clients and mobile apps save most of their bandwidth.

- **Always correct:** a `304` is sent only when the caller would receive exactly the same body. Rules run on every request, so lost access gives `404`, never `304`.
- **Writes unchanged:** `If-Match` keeps comparing document versions.
- **Private by default:** browsers and the JS client revalidate, and shared caches never store user data. Public caching is an explicit opt-in for anonymous reads.
- **The saving is the body:** queries still run. For frequently changing lists, realtime is the better tool.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Document `ETag` | `"<version>"` as today (`"12"`), or `"<version>.r<hash>"` when the representation differs (redacted fields, future field selection); an `If-Match` carrying either form keeps working |
| `If-None-Match` | compares the whole tag; `304` with `ETag` and caching headers, no body |
| `If-Match` | compares the version part only, as today |
| List `ETag` | weak: `W/"<hash>"` over the ids, versions and representation tags returned, the `next` cursor and the total |
| Expanded responses and responses with signed file links (`file_links=true`) | never `304`, never cached by the client; `private, no-cache` |
| Headers | `private, no-cache` by default; `Vary` already says `Authorization, Cookie` (and `Origin` for CORS); `If-None-Match` joins the CORS allowed request headers |
| Public caching | `cache: { public: true, max_age }` in `collection.yaml`, anonymous requests only (no `Authorization`, no session cookie), the default view only, checked at startup by analysing the read rule |
| JS client | built-in bounded cache, automatic `If-None-Match`, on by default for reads |
| Later | skipping the query when nothing changed, once realtime's change tracking exists |

## 3. Documents (issue 20.1)

```
GET /v1/{realm}/{db}/{coll}/{id}
→ 200, ETag: "12", Cache-Control: private, no-cache, Vary: Authorization, Cookie

GET … If-None-Match: "12"
→ 304, ETag: "12"            (rules evaluated; nothing changed for this caller)
```

- **Representation tag:** when the response differs from the full document for this caller (fields redacted by `reveal`; any future field selection on reads), the `ETag` gains a suffix: `.r` plus the first 8 hex characters of SHA-256 over the sorted list of hidden or selected fields (`"12.r1a2b3c4d"`). Nothing redacts a field today, so for now every tag is the bare version; the mechanism and its tests are in place for `reveal` (16.4). A caller whose view changes gets a new tag and a full response.
- **Order of checks:**
  1. read the document;
  2. evaluate the read rule (`404` if denied or missing, as today);
  3. compute the representation;
  4. compare with `If-None-Match` (any of the listed tags, or `*`);
  5. answer `304` or `200`.
- **`If-Match` on writes** compares only the version part, so a tag with a suffix still works for updates. Versions are written as they are today (`"12"`, or the bare number): nothing a client already stores changes.
- **Comparison is weak**, as `If-None-Match` requires: `W/"12"` matches `"12"`.
- **`304` responses** carry `ETag`, `Cache-Control` and `Vary`, as the standard requires.
- **Applies to:** document reads through the API and the admin data API, which share the handlers.
- **Never `304`:** a response with signed file links (`file_links=true`): the links are made per response and expire, so the version doesn't describe what the caller receives; it keeps its `ETag` for `If-Match` and carries `Cache-Control: private, no-store`.
- **`_batch` has no read operations** (every operation writes), so nothing changes there.

## 4. Lists (issue 20.2)

- **Weak validator:** `W/"<hash>"`, where the hash covers, in order, every returned document's id, version and representation tag, plus `has_more`, the `next` cursor and the total when included.
- **The query and rules always run.** A document added, removed, edited, hidden or revealed for this caller changes the hash, so `If-None-Match` is always correct.
- **`304`** when the hash matches. The saving is the body, often the bulk of the cost on mobile networks.
- **Expanded lists** (`expand=…`) and lists with `file_links=true` never get `304`, and keep `private, no-cache` (`no-store` for file links).
- **Later:** skipping the query when the collection hasn't changed since the `ETag` was issued, using realtime's change tracking, which also knows about access changes. Recorded as a follow-up.

## 5. Caching headers and public caching (issue 20.3)

**Default** for documents and lists: `Cache-Control: private, no-cache` and `Vary: Authorization`. Browsers and the JS client may keep responses but revalidate every time, and shared caches never store them.

**Opt-in public caching** for content anyone can read:

```yaml
# collection.yaml
cache:
  public: true
  max_age: 60          # seconds; at most 3600
```

- **Anonymous requests only:** applies only to requests without `Authorization`, on documents and lists the anonymous read rule allows. They get `Cache-Control: public, max-age=<n>` plus the `ETag`, so CDNs can serve them and revalidate.
- **Authenticated requests** to the same collection stay `private, no-cache`. A request counts as authenticated when it has an `Authorization` header **or a session cookie**; `Vary: Authorization, Cookie` keeps the two apart in caches.
- **Only the default view and plain reads:** the trash view (`deleted`), expanded responses and `file_links` are never public.
- **Startup refuses** `public: true` on:
  - collections whose read rule can't allow anonymous access. A rule can depend on `document`, so evaluating it once with no user would wrongly refuse `document.published == true`: the check reads the rule's syntax tree with three values (true, false, unknown): `user == nil` is true, `user != nil` and any use of `user`'s fields false, `document` and `now` unknown; `and`, `or` and `not` combine them. The rule is refused only when it is false for every document;
  - collections with `reveal` fields;
  - collections whose read rule depends on time (it mentions `now`).
- **Expanded responses** are never public.
- **Security checklist** (in the issue's done-when list):
  - authenticated responses never carry `public`;
  - `Vary` is always present;
  - a document that stops being anonymously readable stops being served publicly within `max_age`, which the docs state;
  - no user data in public responses, by test.

## 6. JavaScript client (issue 20.4)

- **Cache:** the last body and `ETag` per URL and session, in memory, bounded (200 entries or 5 MiB by default, least recently used dropped first). Only `GET`s of documents and lists are kept; responses with expansions or signed file links are not.
- **In functions:** the client `ctx.db` uses runs with the cache off: an invocation is short and acts as one caller.
- **Automatic:** `If-None-Match` is sent for cached reads, and on `304` the cached body is returned. For debugging, `client.cacheStats()` reports hits, misses, entries and bytes, and the low-level `client.request()` result says `fromCache`.
- **Clearing:** on sign-out and user change. After a successful write, the document's entry is dropped; list entries need no marking — every read revalidates anyway (`no-cache`).
- **Turning it off:** `cache: false` per client or per call. Expanded requests are never cached.
- **Realtime:** subscriptions (27.16) reuse it for their initial loads.

## 7. Documentation (issue 20.5)

- **How it works:** `ETag`s, representation tags, why the query still runs, how `If-Match` and `If-None-Match` differ.
- **Clients:** the built-in cache, and doing it by hand with raw HTTP (mobile apps until platform clients exist).
- **Public caching:** when to use it, CDN setup, `max_age` and how long a hidden document can remain cached.
- **Polling versus realtime:** a short guide to choosing.

## 8. Issues

| id | Issue | Depends on |
|---|---|---|
| 20.1 | Conditional GET for documents | — |
| 20.2 | Conditional GET for lists | 20.1 |
| 20.3 | Public caching for anonymous reads | 20.1 |
| 20.4 | Cache in the JavaScript client | 20.2 |
| 20.5 | Caching documentation | 20.3, 20.4 |

**Gaps found before building (2026-10-14):** the `"v"` prefix would have changed a format clients store; `_batch` has no read operations; signed file links vary per response; the CORS allow-list lacked `If-None-Match`; evaluating a read rule with no user can't tell "needs a document field" from "needs a user"; cookie sessions count as authenticated. All are decided above.

**When to build:** early, right after MFA (13) and consents (19.3–19.6), before organizations. Reference expansion (15.2) already keeps expanded responses private with no `304`. Field encryption's `reveal` (16.4) feeds redacted fields into the representation tag.

## 9. Alternatives considered

- **A hash of the whole body as the `ETag`:** rejected because writes would need a second header.
- **Version only, with no `304` for redacted documents:** rejected because those are often the documents mobile apps poll.
- **No validators for lists:** rejected. **Skipping the query when the collection is unchanged:** deferred until realtime's change tracking exists.
- **Private only, with no public option:** rejected because public content needs CDNs. **`max-age` for authenticated responses:** rejected because it serves stale data and data the user lost access to.
- **The HTTP cache only, or exposing `ETag`s to apps:** rejected for a built-in client cache.

## 10. Follow-ups

- Skipping list queries when nothing changed, using realtime's change tracking (including access changes).
- `Last-Modified` and `If-Modified-Since`, if a client needs them.
- Conditional GET for files is already handled by storage signed links and the files design's cache settings.
