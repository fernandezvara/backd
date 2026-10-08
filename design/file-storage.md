# Design: files linked to documents, stored in S3-compatible storage

- **Issues:** design #147 (8.0); #148 (8.1) storage per realm, #149 (8.2) file fields and proxy transfers, #150 (8.3) create with files, #151 (8.4) direct uploads, #152 (8.5) erasure, reconcile and usage, #153 (8.6) files in functions, #154 (8.7) JS client and example app, #155 (8.8) security review, #165 (8.9) tutorial; backlog #159 (7.35) image versions, #160 (7.36) quotas, #166 (7.43) direct uploads above 5 GiB
- **Status:** decided
- **Related designs:** [account-lifecycle.md](./account-lifecycle.md) (erasure, `collection.yaml`), [internal-functions.md](./internal-functions.md), [email-delivery.md](./email-delivery.md) (`retry:`), identity providers (Phase 9, #141; written when that phase starts), [tutorial.md](./tutorial.md) (Shelf chapters 13–15)

## 1. Goal and principles

Let a document carry files — an avatar, receipts, attachments — stored in **S3-compatible storage from a built-in list of supported providers** (AWS S3, MinIO, Cloudflare R2, DigitalOcean Spaces; §3.1), with the **same access rules as the document**, and nothing to build per app.

- **Every file belongs to a document field.** No file area outside documents; public assets or personal file spaces are ordinary collections whose documents carry the file.
- **Config declares, backd enforces:** file fields, sizes, types and upload modes per collection.
- **Server-owned metadata:** file details in a document are written by backd only, like `_meta`.
- **Storage is a realm concern:** backd itself has no storage; each realm brings its own.
- **The bucket is always private;** access goes through backd's rules.
- **Only operations every supported provider has:** PutObject, multipart upload, GetObject (incl. range), HeadObject, DeleteObject, ListObjects (manual reconcile only), presigned GET and PUT, all signed with AWS Signature Version 4 (`AWS4-HMAC-SHA256`). Direct uploads additionally need the `x-amz-checksum-sha256` header (§6.2).
- **Providers are a closed list:** what each provider supports is data in one place in backd (§3.1), never probed at run time; a provider not on the list fails the configuration check, and adding one is one entry plus its docs and smoke test.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Storage config | **per realm only** (`storage:` in `realm.yaml`), keys in the realm's encrypted secrets store; no instance-level storage |
| Providers | built-in list (`aws`, `minio`, `r2`, `digitalocean`) with their capabilities in one place; `provider:` required; anything else, or a feature the provider lacks, fails the configuration check |
| File fields | declared in `collection.yaml` under `files:`; not declared in `schema.json` |
| Uploads | `upload: proxy` (default: streamed through backd) or `upload: direct` (signed PUT link straight to the bucket with a required SHA-256 signed in, up to 5 GiB, verified on completion) |
| Endpoints | uniform: `POST …/_files/{field}` adds (replaces on single fields), `GET`/`DELETE …/_files/{field}/{file_id}` act on one file, `DELETE …/_files/{field}` clears |
| Downloads | redirect to a signed GET link by default; `download: proxy` per realm or field streams through backd, with `Range` and conditional requests |
| Links for apps | `?link=json` on the download endpoint returns `{ url, expires_at }`; `?file_links=true` on document reads adds `url` and `expires_at` to every file; proxy-mode links are HMAC-signed backd links, so both modes give a URL usable in `<img>`, `<video>` or `<a>` without credentials |
| Creating with files | upload first (pending upload), reference it on create/update: `{ "upload": "<id>", "token": "<token>" }` |
| Object keys | one key per file, set at upload: `<prefix>/<realm>/<db>/<coll>/<file id>`; never copied or moved; `prefix` required and unique per instance |
| Access | read the file ⇔ read the document; upload/replace/delete ⇔ update the document, evaluated with `data` and `changed()` like any update (§5.1), so the collection's rules can reserve a file field for functions |
| Network | backd connects **directly and only** to declared storage endpoints (documented exception to "everything through egress") |
| Consistency | every upload journaled; the bucket is never listed except by a manual `reconcile`, which never touches in-flight or recent objects |
| Image versions | not built in; functions can make them with the files API; built-in version is backlog |
| Public files | rules decide; signed links up to 7 days; CDN only through proxy mode for anonymously readable documents |
| Quotas | usage totals counted from day one; enforcement is backlog |

## 3. Configuration

**Per realm** (`realm.yaml`), written commented by `backd template realm` with one example per provider in §3.1:

```yaml
storage:
  provider: r2                     # aws | minio | r2 | digitalocean (§3.1)
  endpoint: https://<account>.r2.cloudflarestorage.com
  # public_endpoint: http://localhost:9000  # minio only: the address signed links use when browsers can't reach `endpoint`
  region: auto
  bucket: acme-files
  prefix: prod                     # required; unique per backd instance sharing the bucket (prod, staging, …)
  access_key: secret:STORAGE_ACCESS_KEY
  secret_key: secret:STORAGE_SECRET_KEY
  download: presigned              # presigned | proxy (realm default)
  presigned_ttl: 5m                # realm default for signed links
  pending_ttl: 1h                  # lifetime of unused pending uploads
```

**Per collection** (`collection.yaml`):

```yaml
files:
  avatar:
    max_size: 2MiB
    types: [image/png, image/jpeg, image/webp]
  receipts:
    multiple: true
    max_files: 10
    max_size: 10MiB
    types: [application/pdf, image/*]
  video:
    upload: direct                 # proxy (default) | direct
    max_size: 2GiB
    types: [video/mp4]
    download: proxy                # overrides the realm default
    presigned_ttl: 7d              # up to 7 days
    cache: 1h                      # Cache-Control max-age in proxy mode, only for anonymously readable documents
```

### 3.1 Supported providers

One table in backd (`internal/storage`, on top of an existing S3 client library) holds every provider's capabilities; the code reads it instead of branching on provider names or probing. The same settings apply to every provider; the entry decides how they are used.

| Provider | `provider:` | Addressing | Endpoint check | Region | `x-amz-checksum-sha256` (direct uploads) |
|---|---|---|---|---|---|
| AWS S3 | `aws` | virtual-hosted | `https://s3.<region>.amazonaws.com` | the bucket's region | yes |
| MinIO | `minio` | path style | any HTTPS URL (HTTP allowed in development) | any, default `us-east-1` | yes |
| Cloudflare R2 | `r2` | virtual-hosted | `https://<account>.r2.cloudflarestorage.com` | `auto` | to be verified in 8.1 |
| DigitalOcean Spaces | `digitalocean` | virtual-hosted | `https://<region>.digitaloceanspaces.com` | the Space's region | yes (documented in the [Spaces API reference](https://docs.digitalocean.com/reference/api/spaces/)) |

Each entry also records:
- the largest single PUT (5 GiB everywhere today);
- whether `backd storage check` can read the bucket's CORS and encryption status;
- the client library settings it needs, for example sending request checksums only when required, since recent AWS SDKs send CRC checksums by default and some S3-compatible services reject them;
- for self-hosted providers (`minio`), the oldest tested version, which the docs state;
- whether `public_endpoint` is allowed (`minio` only; hosted providers have one address for everyone).

**`public_endpoint`:** backd always connects to `endpoint`. When `public_endpoint` is set, signed links (downloads and direct-upload PUTs) are signed for that address instead, since signing is local and the signed host must be the one the browser uses. backd never connects to `public_endpoint`, so it is not part of the endpoint restriction (§10). Typical use: MinIO in Docker, reached by backd at `http://minio:9000` and by browsers at `http://localhost:9000` or a public hostname. Bucket CORS must allow the app's origin on the public address; `backd storage check` prints both addresses and the host of a sample signed link.

An entry's capabilities are released only after the compatibility smoke test (§13) passes against that provider; a capability that fails is marked unsupported (for example a provider without checksum support allows proxy fields only). `backd storage check` still runs a real upload, checksum and download against the configured bucket, catching self-hosted versions older than the entry assumes.

**Encryption at rest** is the bucket's default encryption, configured at the provider (documented per provider); backd sends no encryption headers and has no `sse` setting. `backd storage check` reports the bucket's encryption status when the provider exposes it.

**Startup checks:** a realm that declares file fields must have `storage:` with a non-empty `prefix` and a `provider` from §3.1 (any other value fails); `endpoint` and `region` must match the provider's entry; `public_endpoint` only on a provider whose entry allows it, with the same scheme rules as `endpoint`; a missing referenced secret → warning and `503 storage_unavailable` for that realm's file endpoints; `max_size` within `BACKD_MAX_UPLOAD_BYTES` for proxy fields and ≤ 5 GiB for direct fields (the single signed PUT limit); `presigned_ttl` ≤ 7 days; a file field must not also be declared in `schema.json`; `types` well formed; `upload: direct` only on a provider whose entry supports `x-amz-checksum-sha256`. backd injects the file fields' schema (object or array of objects) into the API and MongoDB validators; `required` in `schema.json` may name a file field.

**Changing `files:`:** tightening `max_size`, `types` or `max_files` applies to new uploads only; existing files stay valid and are served. Removing a field leaves storage untouched: the stored metadata becomes ordinary inert data in the documents (no longer server-owned, not served through `_files`), and `reconcile` no longer treats its objects as referenced, so `reconcile --delete` removes them under the rules of §7.

## 4. Data model

In the document (server-owned, returned, ignored on writes except as a pending-upload reference):

```json
"receipts": [
  { "id": "fl_d3a1…", "name": "receipt.pdf", "size": 48213, "type": "application/pdf",
    "sha256": "9f2c…", "uploaded_at": "2026-10-04T10:12:00Z" }
]
```

- `type` is the **detected** content type, not the client's claim; `name` is the sanitized display name: taken from `?name=` or `Content-Disposition` (proxy) or the start call (direct), normalized to NFC, path components and control characters stripped, whitespace collapsed, capped at 255 bytes; an empty result becomes `file`. Names are never rejected.
- Object keys: `<prefix>/<realm>/<database>/<collection>/<file id>`, assigned when the upload starts, for proxy, direct and pending uploads alike. Attaching a file only updates the document; objects are never copied or moved. The key does not contain the document id (the metadata and journal do). User file names never reach keys.
- With `?file_links=true` on a read, each file object also carries `url` and `expires_at` (§6.4). They are computed per response, never stored, ignored on writes, and not part of the document's `ETag`, which stays tied to `_meta.version`.
- System collections in `<realm>___system`: `file_journal` (§7), `file_deletions` (queue), `storage_usage` (totals, §9).
- Declared file sub-fields (`receipts.type`, `avatar.size`, …) are usable in `where` and `order_by`.

## 5. Endpoints

| Method and path | Purpose | Access |
|---|---|---|
| `POST /v1/{realm}/{db}/{coll}/{id}/_files/{field}` | add a file, proxy fields: replaces on a single field, appends on a multiple field | `update` |
| `POST /v1/{realm}/{db}/{coll}/{id}/_files/{field}/uploads` | start a direct upload for an existing document, direct fields | `update` |
| `POST /v1/{realm}/{db}/{coll}/_files/{field}/uploads` | create a pending upload (no document yet), proxy or direct | see §6.3 |
| `POST /v1/{realm}/{db}/{coll}/_files/{field}/uploads/{upload_id}/complete` | finish a direct upload | `upload_token` |
| `GET /v1/{realm}/{db}/{coll}/{id}/_files/{field}/{file_id}` | download (redirect or stream); `?link=json` returns the link instead | `read`, or a valid signed backd link (§6.4) |
| `DELETE /v1/{realm}/{db}/{coll}/{id}/_files/{field}/{file_id}` | remove one file | `update` |
| `DELETE /v1/{realm}/{db}/{coll}/{id}/_files/{field}` | clear the field | `update` |

- `_files` is a reserved segment. `PUT` is not used. The same shapes apply to single and multiple fields; clients never need a field's cardinality to download or remove a file.
- Proxy uploads send the raw body with `Content-Type`; the name in `Content-Disposition` or `?name=`. Each field has one upload mode: proxy uploads to a direct field, or `…/uploads` starts on a proxy field for an existing document, answer `409 upload_mode_mismatch`.
- Adding to a multiple field that already holds `max_files` answers `409 too_many_files` before any bytes are stored; `413` is only for size.
- `?file_links=true` is accepted by every document read (single document, list and query) and adds links to every file field of every returned document.
- Every upload record (pending or direct) returns an `upload_id` and a secret `upload_token`, stored hashed; the token is required by `complete` and when attaching (§6.3).
- Every change to a document's files increments `_meta.version`, accepts `If-Match` and returns the updated document with its `ETag`.

### 5.1 Rules for file operations

File operations use the collection's ordinary rules (the `rules:` of its `collection.yaml`); there is no separate permission system.

- **Rules know file fields.** The schema backd injects for file fields counts for the rules check, so `document.file.type`, `data.attachments` or `'thumbnail' in changed()` are valid at startup.
- **Uploads, replacements and removals through `_files`** evaluate the `update` rule with `document` = the stored document and `data` = the document as it will be after the operation (field replaced, appended to, one file removed, or cleared). `changed()` therefore contains the field. The rule runs **before any byte is stored**; the new file's metadata in `data` holds what is known then:
  - proxy uploads: `id`, `name`, and `size` from `Content-Length` (`null` without it); `type`, `sha256` and `uploaded_at` are `null` (allowed types are enforced by `types:` after detection, §6.1);
  - direct uploads: `id`, `name`, `size`, `type` (claimed) and `sha256` from the start call; `uploaded_at` is `null`.
- **Pending uploads referenced on create or update** are checked by the `create`/`update` rule on the final document (§6.3); on create there is no `changed()`, so a rule refuses a field with `data.<field> == nil`.
- **`ctx.admin.db` and API keys skip rules** for file operations as for documents.

**Fields only functions write** (a generated thumbnail, a download counter) are protected with the same pattern as any server-owned field: refuse them in `create` and `update`, and write them from a function through `ctx.admin.db`:

```yaml
create: user != nil && data.thumbnail == nil
update: >
  user != nil && document._meta.owner == user.id
  && !('thumbnail' in changed())
```

A client's upload to or removal from `_files/thumbnail` then answers `403`; the function's `ctx.admin.db … files(id, 'thumbnail').put()` succeeds.

## 6. Flows

### 6.1 Proxy upload (default)
1. Authenticate; load the document through the read filter (unreadable → `404`); evaluate `update` with `data` = the document after the change and the new file's known metadata (§5.1) → `403`. Nothing is stored before the rule allows it.
2. `max_files` check (`409 too_many_files`) and `Content-Length` check (`413`).
3. Journal entry `writing`; stream to storage (multipart above ~8 MiB), counting bytes and computing SHA-256; abort and clean up if the limit is crossed.
4. Detect the type from the first bytes; not allowed → delete the object, `415 unsupported_file_type`.
5. Conditional document update (retries like PATCH); journal `attached`; a replaced file is queued for deletion. If the document write fails, the object is queued for deletion and the journal marked `failed`.

### 6.2 Direct upload (`upload: direct`)
1. `…/uploads` with `{ name, size, type, sha256 }`, all required (`size` ≤ `max_size` ≤ 5 GiB): `update` rule checked as in §5.1 (existing documents) and limits checked, journal entry created, response with a **signed PUT link** (content length, type and `x-amz-checksum-sha256` signed in, valid a few minutes), an `upload_id` and an `upload_token`.
2. The client uploads straight to the bucket (the bucket needs CORS for the app's origins; `backd storage check` verifies CORS and checksum-header support). Storage itself rejects an upload whose length, type or checksum doesn't match what was signed.
3. `…/uploads/{upload_id}/complete` with the `upload_token`: one `HEAD` with checksum mode enabled (size and stored SHA-256 must match the declared values) and one range GET of the first bytes to detect the type, regardless of file size. The `ETag` is recorded in the journal for `reconcile` and debugging. Then the document is updated (existing-document uploads) or the pending upload is marked complete. Any mismatch deletes the object and rejects. `sha256` in the file metadata is always verified by storage, never trusted from the client.
4. **Files above 5 GiB** (the single signed PUT limit) go in parts, for providers with `MultipartSHA256` support (AWS S3, R2, MinIO; Spaces stays unverified, so unsupported). The start call carries `part_sha256` (the SHA-256 of each part) instead of `sha256`; the part size follows from the size (64 MiB, or the next multiple of 16 MiB keeping the file within 1000 parts), so `max_size` is at most 4 TiB. backd creates the multipart upload (`ChecksumAlgorithm: SHA256`) and signs one `UploadPart` link per part with its length and digest (links and upload last at least 24 h, at most 7 d). Completing is `ListParts` (declared sizes and digests, else `409 file_not_uploaded` or `422` + abort), `CompleteMultipartUpload`, then a `HEAD` whose composite checksum (`base64(sha256(part digests concatenated))-N`) must be the one computed from the declared digests. A storage has no full-object SHA-256 for a multipart object, so the file's recorded `sha256` is the hex of that composite digest, known from the start. Unfinished uploads are aborted by the worker's journal cleanup before the object is queued for deletion. JPEG/PNG sent in parts need `keep_metadata: true` (rewriting is not done at this size).

### 6.3 Pending uploads (create with files)
- `POST /v1/{realm}/{db}/{coll}/_files/{field}/uploads` creates a pending upload tied to collection and field, expiring after `pending_ttl` (1 h), and returns `upload_id` and `upload_token`. Proxy fields stream the body; direct fields get a signed link and use `complete`.
- Creating one needs only the right to write to the collection in principle (authenticated, or anonymous if the `create` rule admits anonymous callers); per caller, at most 20 open pending uploads (fixed, not configurable) and a rate limit via the shared counters.
- On create or update, `"<field>": { "upload": "<id>", "token": "<token>" }` is checked (token matches; for a signed-in creator, same user; same collection and field; complete; unused), replaced by the file details and marked used **in the same write**; the `create`/`update` rule is evaluated on the final document. The same rule applies to every caller, signed in or anonymous. `required` file fields therefore work.
- Unused pending uploads expire and their objects are deleted.

### 6.4 Download
1. Authenticate; read filter (unreadable → `404`, never revealing the file).
2. Default: `302` to a signed GET valid `presigned_ttl` (field → realm → 5 min), forcing `response-content-disposition=attachment; filename=…` and the stored type. backd's response: `Cache-Control: private, no-store`.
3. `download: proxy`: stream with `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox`; `Cache-Control: public, max-age=<cache>` only when the document is readable anonymously, otherwise `private, no-store`. Single-range `Range` requests are passed to a range GetObject (`206`, `Accept-Ranges: bytes`, `416` when out of bounds); multi-range requests get the full file (`200`). A strong `ETag` derived from `sha256`; `If-None-Match` answers `304` and `If-Range` is honored.
4. A referenced object missing from storage → `404 file_missing`.
5. **Links for apps:** `?link=json` answers `200 { "url": "…", "expires_at": "…" }` after the same rule check, instead of redirecting or streaming. `?file_links=true` on document reads puts the same pair in each file object. Lifetime is `presigned_ttl` (field → realm → 5 min) in both modes. Generating links is a local calculation (no storage call), so lists can carry them cheaply.
6. **What `url` is:** with `download: presigned`, the storage-signed GET link (as in step 2). With `download: proxy`, a signed backd link: `…/_files/{field}/{file_id}?exp=<unix>&sig=<hmac>`. The HMAC is made with a per-realm signing key that backd generates and keeps in the realm's encrypted secrets store; it covers realm, database, collection, document id, field, file id and expiry. Nothing is stored per link; the link is reusable until it expires, so `Range` requests, seeking and retries work. A request with a valid signature skips authentication and the `read` rule (the rule was checked when the link was issued); an invalid or expired signature answers `403 invalid_file_link`. Rotating the realm's signing key invalidates all outstanding proxy links. Responses follow step 3 (headers, `Range`, `ETag`, caching).

## 7. Consistency, deletion, erasure

- **Journal for every upload** (`file_journal`): `writing → stored → attached`, or `failed`; holds realm, database, collection, field, document id (if any), caller, key, size, expiry.
- **Worker jobs:** delete objects of entries left `writing`/`stored` past a grace period (1 h) and of expired pending uploads; process `file_deletions` with `retry:` (replaced files, cleared fields, deleted documents, erasure).
- **The bucket is never listed** in normal operation. `backd storage reconcile --realm <r> [--delete]` lists the realm's file keys (`<prefix>/<realm>/<database>/…`; other areas such as `_logs/` are never listed) and reports (or deletes) objects that no declared file field references — for use after restoring a database backup or removing a file field. It always skips objects with a journal entry that is not `failed`, objects of unexpired pending uploads, and objects younger than 24 h (by `LastModified`; fixed, not configurable). The report lists exactly what `--delete` removes.
- **Erasure** (`on_owner_delete`): `delete` → the document's files queued; `anonymize` with a file field in `remove` → its files queued; `keep` → untouched.
- **Backups:** MongoDB backups don't include files; the docs recommend bucket versioning/lifecycle at the provider and `reconcile` after a restore.

## 8. Functions

`ctx.db(...).collection(...).files(id, field)` and the same under `ctx.admin.db`: `get()` (stream or bytes), `put(data, { name, type })`, `delete()`, `link(fileId)`. Same rules (for `ctx.db`), limits, type detection, journal and identity as HTTP uploads. `link(fileId)` returns `{ url, expires_at }` exactly like `?link=json` (§6.4): under `ctx.db` the `read` rule is checked for the calling user, under `ctx.admin.db` it is skipped. Under `ctx.db`, `put()` and `delete()` evaluate the `update` rule as in §5.1; `ctx.admin.db` skips it, which is how a function writes a field the rules reserve for it (§5.1).

Functions are invoked with `POST` and return JSON, so a function cannot start a download itself; it returns the link and the app opens it. Cookbook examples:
- making a thumbnail with an npm image library and storing it through `ctx.admin.db` in a field the rules refuse to clients (§5.1);
- a **download function**: takes `{ document_id, field, file_id }`, applies its own logic (counts the download, checks a license, picks the latest asset of a release), and returns `link(fileId)`; the app opens `url` as in §12.

## 9. Usage totals

- `storage_usage` keeps running totals of bytes and file count **per realm** and **per user** (owner of the document), updated from the journal and deletion queue.
- Visible in `GET /_admin/storage` (also: configuration status without secrets, endpoint reachability, queued deletions, stale journal entries) and `backd storage usage --realm <r>`.
- Quota enforcement is backlog 7.36.

## 10. Security

- **Private bucket, scoped credentials:** the key needs Put/Get/Head/Delete (and List for `reconcile`) on the realm's prefix; per-provider docs. Each backd instance sharing a bucket uses its own `prefix`; two instances on the same prefix would collide and `reconcile` on one could delete the other's files (documented in the template and the docs).
- **Network:** backd's storage client connects only to declared endpoints, checked **after DNS resolution**; the production reference gives backd a route to the declared storage only, and the network test proves nothing else is reachable through it. `public_endpoint` is only written into links and never connected to. `decisions.md` records this exception to "everything through egress".
- **Malicious files:** type detected from bytes; HTML, SVG and other active types only when explicitly listed and always served as `attachment`; signed downloads come from the storage origin, never backd's; proxy downloads sandboxed.
- **Rules:** every file write is an update checked before any byte is stored (§5.1); fields that only functions should write are refused to clients with `changed()` in `update` and `== nil` in `create`, and the docs show this wherever a function writes a file field (thumbnails) or a counter.
- **Names:** never in keys; display names sanitized.
- **Pending uploads:** bound to a secret `upload_token` stored hashed (and to the user when signed in); a leaked `upload_id` alone cannot attach or complete an upload.
- **Limits:** `BACKD_MAX_UPLOAD_BYTES` for proxy uploads (separate from `MAX_BODY_BYTES`), 5 GiB for direct uploads, per-field `max_size`/`max_files`, per-caller pending-upload limits and rate limits.
- **Links:** storage-signed and backd-signed links both grant access until they expire, even if the rules change afterwards (documented; keep `presigned_ttl` short for sensitive fields). backd-signed links are bound to one file, use a per-realm key and are verified in constant time.
- **Public files:** only through rules; signed links ≤ 7 days; CDN caching documented with its caveat (cached copies outlive a later rule change).
- **Malware scanning:** out of scope; possible later as an internal-function hook on upload.
- A security review of the file surface before production use.

## 11. Open implementation details

None; all items are decided above.

## 12. JavaScript client

```js
const doc = await expenses.uploadFile(id, 'receipts', file, { ifMatch: etag, onProgress });
const up  = await expenses.prepareUpload('receipts', file);         // pending upload (proxy or direct, handled)
await expenses.create({ title: 'Taxi', receipts: [{ upload: up.id, token: up.token }] });
const url = await expenses.fileUrl(id, 'receipts', fileId);         // ?link=json; { url, expiresAt }
button.onclick = () => expenses.downloadFile(id, 'receipts', fileId); // fileUrl() + location.assign()
const one = await expenses.get(id, { fileLinks: true });            // every file carries url and expires_at
await expenses.deleteFile(id, 'receipts', fileId);
```

`downloadFile(id, field, fileId)` gets a link with `fileUrl()` and opens it with `window.location.assign()`; the link forces `attachment`, so the browser downloads and stays on the page. Browser-only: outside a browser it throws.

Direct uploads are handled transparently (signed PUT then `complete`); progress uses `XMLHttpRequest` in browsers. For direct uploads the client computes SHA-256 incrementally while reading the file and sends it with the start call. `prepareUpload()` returns `{ id, token }`.

## 13. Development, tests, docs

- MinIO in the local and test stacks (bucket and CORS created at startup); example realms point at it. The tutorial's starter stack also runs MinIO, published on `localhost:9000` and configured with `public_endpoint`.
- Integration tests: every endpoint and mode; rules (denied, unreadable `404`, anonymous); size and type refusals mid-stream and on `complete`; checksum mismatch rejected by storage; `upload_mode_mismatch` and `too_many_files`; replace on single fields and append on multiple fields; version races; pending references (wrong or missing token, wrong user/field, reused, expired); journal recovery after a killed process; deletion queue; erasure; `reconcile` skipping in-flight, pending and recent objects, and reporting objects of a removed field; downloads in both modes, cache headers, `Range`, `If-None-Match` and `If-Range`; rules on file operations (`data` and `changed()` for add, replace, remove and clear; `403` before any byte is stored; a field reserved with `changed()` refused to clients and accepted from `ctx.admin.db`; `data.<field> == nil` on create with a pending upload); `?link=json` and `?file_links=true` in both modes (links work without credentials, are absent by default, are not in the `ETag`); backd-signed links (tampered, expired, other file, after key rotation); name sanitization; endpoint restriction after DNS; usage totals.
- A compatibility smoke test per provider in §3.1 (MinIO in CI; AWS, R2 and DigitalOcean Spaces run before each release and whenever an entry changes), covering every capability the entry claims.
- Tutorial: Shelf gains files in chapters 13–15 (`design/tutorial.md` §4), using every feature above; the shelf CI replay covers them.
- Docs: "Files" guide, including a **Download buttons** section for pages with many files (for example a releases page):
  - render the list from the document's file metadata, without links;
  - on click, `downloadFile()` (or `fileUrl()` then `location.assign()`), so each link is fresh;
  - `?file_links=true` only for content shown right away (galleries, previews), since links expire with `presigned_ttl` while a page can stay open much longer;
  - pitfalls: a plain `<a href>` to backd's download endpoint has no credentials; `window.open` after an `await` is blocked as a popup; an expired link answers `403` from storage or `403 invalid_file_link` from backd, so get a new one;
  - when the click needs custom logic (counting, licensing), call a download function (§8) and open the `url` it returns;
- "Files" guide, **Rules for files** section: what `data` and `changed()` hold for `_files` calls, rules before bytes, and protecting fields only functions write (§5.1), with the thumbnail and download-counter examples; the rules reference page links to it;
- the rest of the docs: one setup page per provider in §3.1 (AWS S3, MinIO, Cloudflare R2, DigitalOcean Spaces) and how to request a new one, security notes, backups and `reconcile`, CDN guidance, `realm.yaml`/`collection.yaml` references, functions files API, JS client, OpenAPI.

## 14. Proposed issues

| id | Issue | Depends on |
|---|---|---|
| 8.1 (#148) | Storage per realm: config, secrets, client restricted to declared endpoints, `backd storage check`, MinIO in the stacks, network test | — |
| 8.2 (#149) | File fields, proxy upload/download/delete, type detection, journal, deletion queue, public files and caching | 8.1, 4.2c |
| 8.3 (#150) | Pending uploads and references on create/update | 8.2 |
| 8.4 (#151) | Direct uploads | 8.3 |
| 8.5 (#152) | Erasure integration, `reconcile`, usage totals and `GET /_admin/storage` | 8.2, 4.6 |
| 8.6 (#153) | Functions files API and thumbnail cookbook | 8.2, 4.2a |
| 8.7 (#154) | JS client, example app, docs | 8.3, 8.4 |
| 8.8 (#155) | Security review of files | 8.4, 8.6 |
| 8.9 (#165) | Tutorial: files in Shelf (chapters 13–15), starter stack with MinIO, CI replay | 8.5, 8.6, 8.7 |
| 7.35 (#159) | Built-in image versions (backlog) | 8.6 |
| 7.36 (#160) | Storage quotas per realm and per user (backlog) | 8.5 |
| 7.43 (#166) | Presigned multipart direct uploads above 5 GiB (backlog) | 8.4 |

## 15. Alternatives considered

- **Uploads only through backd / only direct:** chose both from the start, proxy by default.
- **Proxy downloads by default / signed links only:** chose signed links by default with proxy as an option.
- **Instance-level storage (only, or as default):** rejected; storage belongs to realms.
- **Multipart create-with-files / no required file fields:** chose pending uploads referenced on write.
- **A file area outside documents:** rejected; one permission and lifecycle model.
- **Storage through egress (always or for public storage):** rejected; declared direct endpoints.
- **Periodic bucket listing:** rejected; journal plus manual reconcile.
- **Built-in image processing now / never:** chose later, functions meanwhile.
- **Public buckets with permanent URLs / no anonymous downloads:** chose rules plus signed links.
- **Quotas now / no totals:** chose totals now, enforcement later.
- **Ownership marker object against shared prefixes:** rejected; `prefix` required and documented as unique per instance.
- **Hybrid checksum verification (streaming when the provider lacks checksum headers) / asynchronous `complete`:** rejected; direct uploads always use a signed SHA-256 and a synchronous `complete`.
- **Presigned multipart now:** rejected for the first version; 5 GiB cap, multipart is backlog.
- **Anonymous-only upload tokens / no anonymous pending uploads:** chose one token rule for every caller.
- **Cardinality-specific verbs (`PUT` single, `POST` multiple):** rejected; uniform endpoints by file id.
- **Automatic cleanup or forbidden removal of file fields:** rejected; removed fields become inert and `reconcile` cleans up.
- **`sse` setting:** rejected; bucket default encryption at the provider.
- **Any S3-compatible endpoint, with features probed at run time:** rejected; a closed provider list with capabilities in one place, so a configuration either works or fails the check.
- **Copying pending objects to a document key on attach:** rejected; one key per file, set at upload.
- **Links only through the redirect / always inside documents:** rejected; `?link=json` for one file and opt-in `?file_links=true` for documents.
- **Single-use tokens or cookies for proxy links:** rejected; single-use breaks media `Range` requests, cookies need new auth machinery; stateless HMAC-signed links instead.
