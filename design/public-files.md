# Design: public files

- **Issues:** milestone 36-public-files: design #729; #730 config, #731 storage chooser, #732 upload and url, #733 versions, #734 erasure/usage/exports, #735 clients/functions/admin UI, #736 dev stack and smoke tests, #737 docs and security
- **Status:** decided
- **Related designs:** [file-storage.md](./file-storage.md) (everything here builds on it), [image-versions.md](./image-versions.md), [export-import.md](./export-import.md), [conditional-get.md](./conditional-get.md), [metrics.md](./metrics.md), [realm-layout.md](./realm-layout.md)

## 1. Goal and principles

Let a file field be **public**: the document stores the file and carries its permanent public URL, so an application can put it in the page as soon as the document arrives, with no signing and no call to backd. Meant for images that are not private (galleries, product pictures, avatars people choose to show).

- **Per field.** A field is public or it is not. Someone who needs both declares two fields (and a boolean next to them if the application wants a switch). No per-file visibility.
- **A second, independent storage.** A realm that wants public and private files configures two storages. Nothing is shared with the private one except the code: a public bucket is a different bucket, possibly a different provider (for instance R2 behind a CDN for public files, MinIO for private).
- **Backd never connects to the public URL.** It writes to the public storage's `endpoint`; browsers read from `url` (a CDN domain, usually). Whether the bucket really is readable by everyone, and listing is off, is the operator's job, documented as a must and tested by them in advance.
- **Only safe things are accepted.** First release: JPEG and PNG, nothing else.
- **No migration.** A file stays where it was stored; changing a field's `public` setting only affects new uploads (§9).

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Config | `public_storage:` in `realm.yaml`, same shape as `storage:` plus a required `url`; `public: true` on a file field in `collection.yaml` |
| Bucket | must differ from `storage`'s: same endpoint + bucket (any prefix) is refused at startup |
| Key | `<prefix>/<realm>/<db>/<coll>/<file id>/<filename>`; versions `<file id>/<version>/<filename>` |
| URL | `<url>/<key>`, computed on read, so moving the CDN touches no document |
| Types | JPEG and PNG, detected from the bytes; the name's extension must agree |
| Uploads | proxy only (the bytes are sanitized before they are published); `upload: direct` on a public field fails the configuration check |
| Reads | the file object in the document has `public: true` and `url`; ready versions have their `url` |
| Rules | unchanged: upload/replace/delete = an `update` of the document. Reading the *document* is still rule-gated; reading the *bytes* at `url` is not |
| Private route | `GET .../_files/{field}/{id}` still works for public files (rules-gated, as today) |
| Exports/imports | public files are included everywhere private ones are (account export, realm export/import, anonymize rules); import re-checks and re-strips them before publishing (§8) |
| Erasure | `on_owner_delete` and deletion work exactly as for private files, on the storage the file is in |
| CDN | not ours: copies in a CDN outlive the object; documented as the operator's concern |
| Verification | none by backd; `backd storage check` writes, reads and deletes an object, and prints a URL to open |

## 3. Configuration

```yaml
# realm.yaml
storage:            # private, as today
  provider: minio
  endpoint: https://minio.internal:9000
  bucket: backd-private
  prefix: prod
  ...
public_storage:     # new; optional
  provider: r2
  endpoint: https://<account>.r2.cloudflarestorage.com
  bucket: backd-public
  prefix: prod
  url: https://cdn.example.com   # where browsers GET; backd never connects to it
  # keys are realm secrets, named like the private ones (names to decide in the issue)
```

```yaml
# collection.yaml
files:
  photo:
    public: true
    max_size: 5MiB
    versions:
      thumb: { width: 200, height: 200, fit: cover }
```

Startup (and `config check`) rejects:
- `public: true` without `public_storage`.
- `public_storage` on the same `endpoint` + `bucket` as `storage` (the one mistake that would expose everything).
- `url` that is not `https://...` (http only with `BACKD_DEV=true`); a path is allowed, query and fragment are not; a trailing slash is trimmed.
- `types` on a public field other than `image/jpeg` / `image/png` (default for a public field: those two). `upload: direct` on a public field. `keep_metadata: true` on a public field (a public file is always stripped).
- `public_storage` has its own `public_endpoint` (the existing meaning: the address a browser uses to reach a MinIO that backd reaches at another address); the new `url` is a different thing, which is why it has a different name.

The provider table, the transport rules (only the declared endpoint, resolved by backd, no redirects, no proxy), `prefix` uniqueness per instance and encryption at rest apply to `public_storage` exactly as to `storage`. The provider capability checks that make sense are kept (single PUT limit, proxy upload); direct-upload capabilities are not required.

## 4. Keys and URLs

- Private (unchanged): `<prefix>/<realm>/<db>/<coll>/<file id>`; versions `<file key>/<name>`.
- Public: `<prefix>/<realm>/<db>/<coll>/<file id>/<filename>`; versions `<file id>/<version>/<filename>`.
- The file id is unique per upload, so two `photo.jpg` are `id1/photo.jpg` and `id2/photo.jpg`; replacing a file makes a new id and so a new URL.
- A version's extension is its output format's (a `png` source with a `jpeg` version gives `.../thumb/photo.jpg`). A slash cannot be in a filename, so `<id>/thumb` (a file named `thumb`) and `<id>/thumb/photo.jpg` never collide.
- `url` = `<url>/` + key, each segment percent-encoded. Not stored: computed from the key stored on the file, so `url` can change without touching documents.

### 4.1 Filename

The original name is kept as given in the file's metadata (`name`). The key uses a sanitized form, made once at upload:

1. Unicode NFC; take the last path component (no `/` or `\`).
2. Remove control characters and anything that is not a letter, digit, space, `.`, `-`, `_`, `(`, `)` or `+` in any script (other characters become `_`). Trim spaces and dots at the ends.
3. At most 120 bytes, cut on a character boundary, keeping the extension.
4. If nothing is left, or it is `.`/`..`, the name is rejected (`400 invalid_file_name`), not invented.

The sanitized name is **stored on the file** (`key_name`), not recomputed: the algorithm may change later, and the key of every existing file must stay derivable (exports, reconcile, deletion, versions).

The sanitized name must end in `.jpg`, `.jpeg` (JPEG) or `.png` (PNG), case-insensitive, and **agree with the detected content**. A different extension, a missing one, or a mismatch with the bytes is `400` (`file_extension_mismatch`), never renamed. A type that is not allowed at all stays `415 unsupported_file_type`, as in every field today.

## 5. Uploads

- **Proxy only.** The bytes stream through backd, are type-detected, stripped of metadata (the existing `imaging.Strip`: JPEG/PNG, fail closed with `422 invalid_image`) and stored under the public key. Pending uploads (create with files) work the same, in the public storage, with the journal recording which storage.
- Checks, in order: the name sanitizes (4.1); the declared and the detected type are allowed; the extension agrees; size and pixel limits; strip; store. The same code path serves proxy, pending uploads and replacement.
- Object headers are set on the PUT, because a bucket cannot add them per request: `Content-Type` (detected), `Content-Disposition: inline` and `Cache-Control`. **Originals** get `public, max-age=31536000, immutable` (a replacement is a new id, so a URL's bytes never change). **Versions** get `public, max-age=86400`, without `immutable`: they are overwritten in place by `backd files regenerate` and `generate(params)`, so a CDN must be able to pick up the new copy within a day (an operator can purge sooner). Every provider's smoke test covers that these headers are accepted on PUT and returned on GET.

## 6. The file object in a document

```json
{
  "id": "...", "name": "Holiday photo.jpg", "type": "image/jpeg", "size": 123456,
  "width": 1600, "height": 900,
  "public": true,
  "url": "https://cdn.example.com/prod/main/shop/products/<id>/Holiday%20photo.jpg",
  "versions": { "thumb": { "status": "ready", "url": "https://cdn.../<id>/thumb/Holiday%20photo.jpg", ... } }
}
```

- `public` and `url` only for public files; a version has `url` only when `ready` (a client never renders a broken image).
- Server-owned, like the rest of the file details; readable by anyone who can read the document. Documents already stored need nothing: `url` is computed on every read, by one function applied wherever a file object is serialized: the API, reference expansion, events (triggers/realtime), functions (`ctx.db`) and the admin routes.
- **Conditional GET:** the document's ETag follows `_meta.version`, but `url` depends on configuration. A document that has public files gets the representation tag `conditional-get.md` already defines: `"<version>.r<hash>"`, the hash taken (with any other representation inputs) over `public_storage.url`. Changing the CDN address then gives every such document a new tag, so clients refetch and receive the new `url` instead of keeping a dead address under a `304`. `If-Match` accepts both forms, as today; documents without public files keep the bare version. Applies to lists too (their weak tag already hashes the representation tags).
- OpenAPI, the generated types (per realm), the JS and Dart clients (`fileUrl` returns `url` for a public file), the admin UI and `files().link()` in functions reflect it.

## 7. One place that picks the storage

Today almost everything reaches storage through `realmObjects.For(ctx, realm)` (`internal/httpapi/realm_storage.go`) and `Settings.Storage`. The rule for the implementation: **`For` takes the visibility** (`For(ctx, realm, public)`) and **one function builds the key** for it; no caller may compare `public` itself except to pass it on. Every reference to a stored object carries `public` (bool) beside the key.

Call sites to review one by one (the list is a starting point, must be reviewed in full again):

| Area | Where | Public-aware? |
|---|---|---|
| Upload (proxy), replace, clear | `httpapi/files.go`, `files_pending.go` | **yes** |
| Direct uploads, multipart | `files_direct.go`, `storage/multipart.go` | no: refused for public fields; private only |
| Download, `?link=json`, `file_links`, proxy | `files.go`, `files_account.go` | **yes**: private route works for both; public files also give `url` |
| Versions (job, regenerate, scan) | `worker_image.go`, `worker_regenerate.go`, `files_versions.go` | **yes**: source and copies in the file's storage; version keys per 4 |
| Upload journal, deletion queue and retries | `worker.go` (journal/queue records) | **yes**: records say which storage |
| Erasure (`on_owner_delete`), document delete | `mongodb/erase.go`, `worker.go` | **yes** |
| Usage totals, quotas | `files_quota.go`, usage in `documents` | **yes**: public files count in the same totals |
| Reconcile | `admin_storage_reconcile.go`, CLI | **yes**: runs per storage; `--public` flag selects, default both |
| Storage check | `admin_storage.go`, CLI | **yes**: checks each configured storage |
| Config view and checks | `admin_config.go`, `cmd/backd/main.go` (3 places), `registry/storage.go`, `registry/files.go` | **yes** |
| Functions `files(id, field)` | `files_functions_test.go`, functions API | **yes**: same rules; `link()` returns `url` |
| Account export (the user's own package) | `userexport.go`, `worker_export.go`, `auth_exports.go` | **yes** (§7.1) |
| Realm export and import | `admin_export.go` (`exportFile`), `admin_import.go`, CLI `backd export`/`import` | **yes** (§7.1) |
| Anonymize (`--anonymize-files`) | `admin_export.go`, `mongodb/erase.go` policies | **yes**, same flag (§7.1) |
| Export packages stored by compliance (`_exports/`) | `worker_export.go` | private storage only, unchanged (they are not user files) |

### 7.1 Exports and imports

A public file is still information about the user, and everything that moves a user's or a realm's files includes it:

- **Account export:** the user's package holds public files beside private ones, read from the storage each file's reference names. The documents in the package show `public: true`; `url` is left as exported text and is not authoritative.
- **Realm export:** public files are exported like private ones (`<db>/_files/<file id>`, ids are unique, so no collision), with their SHA-256 in the manifest; `--no-files` skips both. The route that streams a file knows only `<db>/<coll>/<id>`: the object key is rebuilt from the `key_name` stored in the document, so the exporter reads the documents first (it already does) and passes the name along. Credentials: the package needs the master key or explicit credentials for the public storage too (`--public-storage-key`/`--public-storage-secret`, same rules as the private ones). Versions are not exported (the target regenerates them).
- **Realm import:** the target realm must have `public_storage` and a `public: true` field wherever the package has public files; otherwise the import stops before writing (a dry run reports it). A public file is **not trusted because it was exported**: it goes through the same checks as an upload (detected type, extension agreement, sanitized name recomputed and compared with `key_name`, strip, size/pixel limits) and is stored with the headers of §5, because it is about to be published. A file failing a check is reported like an invalid document (`--skip-invalid` to continue). The imported `url` is ignored and recomputed from the target's `public_storage.url`.
- **Anonymize:** public files follow the rule for private ones: excluded unless `--anonymize-files keep`. A public photo of a person is personal data, and a staging copy must not republish it.
- **Erasure and account deletion:** unchanged (§2).

`public_storage` secrets follow the private ones (realm secrets store; names decided in the issue); functions can't declare them (as with the private keys).

## 8. Security

- Public means **unauthenticated and unlisted**: anyone with the URL reads the bytes; file ids are time-ordered, so partly guessable. The document's `read` rule protects the *document*, not the bytes once the URL is known.
- The public bucket must be readable by everyone, **listing must be off**, and nothing private may ever be written to it (backd never does). Testing this before production is the operator's job; backd does not connect to `url`.
- Types: JPEG and PNG only, detected from the bytes; metadata always stripped (EXIF/GPS); a mismatching extension is refused. No SVG, HTML, XML, GIF or WebP in this release (GIF/WebP carry metadata the stripper does not remove yet).
- Serve the CDN domain from a different site than the application, to keep cookies and storage apart.
- Deleting a file deletes the object; a CDN (or browser) cache may keep serving it until its TTL. Operators using a CDN should purge or review deleted files in the bucket; backd does not purge.
- **Pending uploads are public from the moment they are stored.** A create-with-files upload is written before its document exists, so for a public field the object is readable at its URL (known only to the uploader) until it is attached or the pending TTL deletes it. Accepted: the id is not discoverable, and the same person could publish it by attaching it. To state in the docs.
- Imports and `files().put` in functions go through the same type, extension, name and strip checks as uploads; no path writes bytes to the public storage without them.
- **Renaming** the realm, database or collection changes every key and so every URL (the private storage has the same limitation, since keys are rebuilt from the current names). Not supported once files exist; documented, and more important here because URLs are already in pages and caches.
- **CORS:** `<img>` needs none; `fetch` or `<canvas>` on a public image does, so the docs tell operators to set CORS on the public bucket.
- **Cost and abuse:** public bandwidth and hotlinking are billed to the CDN or bucket owner, not handled by backd. The public credentials should be scoped to the public bucket only.
- Update `docs/content/docs/auth/security.md` and `SECURITY.md`; a security review precedes enabling this in production, like file storage.

## 9. Changing a field's setting

No migration in this release. A file records where it is (`public`); the field's current setting decides only where *new* uploads go. Consequences, to document:
- Turning `public` off: existing files stay in the public bucket and keep their `url`; new files are private. Startup logs a warning listing fields whose stored files contradict the config.
- Turning it on: existing files stay private; new files are public.
- Moving files between storages is a future feature that needs its own design.

## 10. Development and examples

- `docker-compose.dev.yml` (and the builder compose): `minio-setup` also creates `backd-public` with `mc anonymous set download`; `public_storage.url` is `http://localhost:9000/backd-public` (MinIO is path-style, so the bucket is in the URL; nginx can hide it, not needed in dev).
- A public gallery field in the `workshop` example realm and a short docs page under `configuration/` (file fields) and `operations/`.

### 10.1 Tests and observability

- **Integration stack:** a second MinIO bucket with anonymous download, and tests that read `url` **without credentials** (the one thing the usual tests never do), plus a check that the private bucket is not readable the same way.
- **Provider smoke tests** (`go test ./internal/storage -run ...`, before each release, like R2's checksum test): `Cache-Control` and `Content-Disposition` accepted on PUT and returned on GET, for each provider that can be a public storage.
- **Metrics** ([metrics.md](./metrics.md)): the storage operation and byte series get a `storage` label (`private`/`public`) so the numbers do not mix.
- **Realm layout** ([realm-layout.md](./realm-layout.md)): `public_storage:` lives wherever `storage:` does, and its secrets next to the private ones; the issue confirms the exact place.

## 11. Order of work (one commit each, to be turned into issues)

1. Config: `public_storage` (next to `storage:` in the realm layout), `public: true`, validations, provider checks, `storage check`, docs.
2. The storage chooser and the `public` flag on journal/queue/usage records, with private behavior unchanged (refactor, tests green).
3. Public upload (proxy, pending), filename rules, type/extension checks, headers, `url` in the file object, OpenAPI.
4. Versions in the public storage, version `url` when ready.
5. Erasure, reconcile, usage; account export, realm export/import and anonymize for public files (§7.1).
6. Clients (JS, Dart), generated types, admin UI, functions `link()`.
7. Dev compose and integration stack (anonymous public bucket), provider smoke tests, metrics label, example realm, tutorial/docs pages, security docs and review.

## 12. Not in this release

GIF/WebP/AVIF/SVG or any non-image; direct uploads to the public storage; per-file visibility; moving files between storages; purging a CDN; verifying that the bucket is public.
