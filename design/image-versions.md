# Design: image versions

- **Issues:** 7.35 (#159, umbrella) → features 11.1–11.7 (milestone `11-image-versions`)
- **Status:** decided
- **Related designs:** [file-storage.md](./file-storage.md) (files, journal, deletion queue, signed links), [internal-functions.md](./internal-functions.md), f6 (job steps)

## 1. Goal and principles

Resized and converted copies of images (thumbnails, previews) **declared in config and made by backd**, without writing a function — and, when an app needs control, generated from functions with the same vocabulary.

- **One engine, in Go, on the worker:** pure Go decoding (memory-safe, no cgo, static binary) and scaling; never on the API instance.
- **Every version is declared:** apps always know where to look; a version's state lives in the document, so nothing asks storage whether it exists.
- **One vocabulary:** the same parameter names in `collection.yaml` and in functions.
- **Bounded resources:** dimensions checked before decoding; limits owned by the operator.
- **Private by default:** metadata (including GPS) removed from versions always and from originals unless kept on purpose.
- Breaking changes are allowed (no installed base): the "thumbnail function" pattern in the examples is replaced.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Where | `versions:` nested under a file field in `collection.yaml` |
| Kinds | parameters → made by workers; parameters + `writable: true` → made by workers, functions may redo; `writable: true` only → made only by functions |
| Parameters | `max_width`, `max_height`, `fit` (`contain` default, `cover`, `stretch`), `quality`, `format`; no enlarging |
| Inputs | JPEG, PNG, WebP; anything else is **skipped**, never an upload error |
| Outputs | `jpeg`, `png`; `webp` only if a suitable pure-Go encoder checks out |
| State | per version in the document: `pending`, `ready`, `empty`, `skipped`, `failed` (+ reason); `width`/`height` stored for versions and image originals |
| Functions | `version(name).regenerate()`, `.generate(params)` (writable only), `.delete()`; no byte upload |
| Limits | `BACKD_IMAGE_MAX_PIXELS` (40 MP), `BACKD_IMAGE_TIMEOUT` (30 s), `BACKD_IMAGE_CONCURRENCY` (derived from container limits); fields may only lower `max_pixels` |
| Config changes | stale versions detected by fingerprint and reported; regenerated on demand by a resumable command |
| Metadata | versions always clean and upright; originals stripped losslessly unless `keep_metadata: true` |

## 3. Configuration (`collection.yaml`)

```yaml
files:
  file:
    max_size: 25MiB
    types: [application/pdf, image/png, image/jpeg, text/plain]
    keep_metadata: false               # default: remove EXIF/GPS and similar from image originals
    max_pixels: 24000000               # optional; may only lower BACKD_IMAGE_MAX_PIXELS
    versions:                          # only for uploads the engine can read; others are skipped
      thumb:   { max_width: 256,  max_height: 256,  fit: cover,   format: jpeg, quality: 80 }
      preview: { max_width: 1280, max_height: 1280, fit: contain, format: jpeg, quality: 85, writable: true }
      watermarked: { writable: true }  # made only by functions
  attachments:
    multiple: true
    max_files: 5
    upload: direct
    max_size: 1GiB
    versions:
      thumb: { max_width: 200, max_height: 200, fit: cover, format: png }
```

**Parameters**

| Key | Meaning |
|---|---|
| `max_width`, `max_height` | bounding box in pixels (at least one required for a version with parameters) |
| `fit` | `contain` (fit inside, keep proportions, no crop — default), `cover` (fill the box, keep proportions, crop centered), `stretch` (fill the box, ignore proportions) |
| `quality` | 1–100, for `jpeg` (and `webp` if available); default 82 |
| `format` | `jpeg`, `png` (`webp` pending verification); default: same family as the source when encodable, else `jpeg` |
| `upscale` | `false` by default: smaller images are not enlarged |
| `writable` | functions may generate this version |

**Startup checks:** a version needs parameters or `writable: true`; `format` must be encodable; `quality` within 1–100; `max_pixels` not above the instance limit; version names follow the usual name rules and are unique per field.

## 4. Data model

Inside a file's details (server-owned, read-only for clients):

```json
"file": {
  "id": "fl_d3a1…", "name": "photo.jpg", "size": 3481220, "type": "image/jpeg",
  "width": 4032, "height": 3024, "sha256": "…", "uploaded_at": "…",
  "versions": {
    "thumb":   { "status": "ready", "id": "fv_…", "size": 18342, "type": "image/jpeg",
                 "width": 256, "height": 256, "params": { "max_width": 256, "max_height": 256, "fit": "cover", "format": "jpeg", "quality": 80 },
                 "fingerprint": "a91c…", "generated_at": "…" },
    "preview": { "status": "pending" },
    "watermarked": { "status": "empty" }
  }
}
```

| Status | Meaning |
|---|---|
| `pending` | waiting for the worker (versions with parameters) |
| `ready` | made; `id`, `size`, `type`, `width`, `height`, `params` (as actually used), `fingerprint`, `generated_at` |
| `empty` | function-only version not generated yet |
| `skipped` | `reason`: `not_an_image` or `unsupported_format` |
| `failed` | `reason`: `too_large`, `decode_error` or `timeout` |

- Object keys: a source keeps file-storage's `<prefix>/<realm>/<db>/<coll>/<file id>`; a version appends one segment — `<file key>/<version name>` (e.g. `prod/shelf/main/assets/fl_d3a1…/thumb`). Version names must be key-safe (usual name rules, no `/` or dot segments), checked at startup. The `fv_...` id is bookkeeping for the journal and `storage_usage`; it never appears in a key.
- Deletion expands, never lists: queuing a file deletes its key plus the derived key of every version the document tracks (`ready`/`failed`), so the bucket is still never listed outside `reconcile`. `reconcile` counts `<file key>/<v>` as referenced only when `<file key>` is referenced and `versions.<v>` holds an id; version writes are journaled like uploads, so in-flight and recent objects are skipped by the usual rules, and their bytes count in `storage_usage`.
- `width`/`height` are also stored on image originals the engine can read.
- Versions are deleted with their source; replacing the source deletes all its versions and resets them (`pending` / `empty`).

## 5. Flow

1. Upload completes (proxy, direct or pending upload attached) → if the file is a readable image, record `width`/`height`, strip metadata unless `keep_metadata` (§8), set versions with parameters to `pending` and writable-only ones to `empty`; otherwise mark versions `skipped`.
2. A job per file (`origin: backd:image.versions`) is queued; a worker claims it.
3. For each `pending` version: read dimensions **before decoding** (refuse above the limit → `failed: too_large`); decode once, apply orientation; scale per `fit`; encode; store at the derived key; record details. One decode serves all versions of the file.
4. The document is updated conditionally (as other system writes, without bumping what clients edit - versions update `_meta.updated_at` but not `_meta.version`, so they never cause `412` for a client editing the document).
5. Failures are recorded per version; transient errors retry with the job `retry:`.

## 6. Limits (worker)

| Setting | Default | Notes |
|---|---|---|
| `BACKD_IMAGE_MAX_PIXELS` | 40 000 000 | checked from the header before decoding; fields can only lower it (`max_pixels`) |
| `BACKD_IMAGE_TIMEOUT` | 30 s | per image |
| `BACKD_IMAGE_CONCURRENCY` | derived: min(CPUs/2, (memory limit × 0.5) ÷ (max pixels × 4 B)), at least 1; logged at startup | explicit value overrides |

`BACKD_MAX_UPLOAD_BYTES` limits the file size; the pixel limit protects against the **decoded** size (a 2 MB file can decode to more than 1 GB). Both are needed.

**Docs — sizing the worker:** memory per concurrent image ≈ pixels × 4 bytes plus the scaled copy; example table (e.g. 40 MP × 2 concurrent ≈ 350 MB for images alone); what happens when undersized (the container is killed for memory, the job is retried by another worker, repeated failures end as `failed`); how to pick `BACKD_IMAGE_MAX_PIXELS` and concurrency for a given memory size.

## 7. Functions

```js
const v = ctx.db('app').collection('assets').files(id, 'file').version('preview');
await v.regenerate();                                              // declared parameters
await v.generate({ max_width: 900, fit: 'cover', format: 'png' }); // other parameters — writable versions only
await v.delete();                                                  // back to pending / empty
```

- Stored files only; results always land in a declared version. No byte upload into versions.
- Same parameter names and validation as `collection.yaml`.
- Runs on a worker through the queue; the function waits within its own time limit and receives the version's new details.
- Errors as `FunctionError` with the status reasons (`too_large`, `unsupported_format`, `decode_error`, `timeout`); `generate` on a non-writable version → `403 version_not_writable`.
- The function's identity applies (`ctx.db` rules or `ctx.admin.db`); changing a version counts as an `update` of the document.
- `@backd/functions-testing` fakes the version API.

## 8. Metadata and orientation

- **Versions:** orientation applied (pixels upright), all metadata dropped — always.
- **Originals:** for JPEG and PNG the engine can read, metadata blocks (EXIF including GPS, XMP, comments, text chunks) are removed **without re-compressing**, unless the field sets `keep_metadata: true`. Proxy uploads: while streaming. Direct uploads: at `complete`, once the declared `sha256` is verified, the worker GETs the object, strips metadata while streaming and PUTs the cleaned body back to the same key (multipart for large ones) — a server-side copy can't change an object's bytes, so none is used. The stored `sha256`, `size` and ETag (journal) are those of the stored (cleaned) file, recorded after the rewrite. Other formats are stored untouched.
- The orientation flag of a cleaned original is preserved or applied (keep only the orientation tag so the original still displays upright).
- Docs and the `collection.yaml` template explain the GPS risk and when `keep_metadata: true` is appropriate (photography, evidence, archives).

## 9. Config changes and regeneration

- Each version stores a fingerprint of its parameters. At startup backd logs new or changed declared versions and the number of files affected.
- `backd files regenerate --realm <r> --db <d> --collection <c> --field <f> [--version <v>] [--missing-only]` and an admin API route queue background jobs: resumable, rate-limited, progress through job steps (f6). Versions functions generated with custom parameters are not touched.

## 10. Downloads and clients

- Signed links are bound to one version: `?link=json&version=<name>` returns a link that serves only that version; changing `?version=` on a signed URL invalidates the signature (`403 invalid_file_link`). In presigned mode the same scoping falls out of the signed object key. A link issued while a version is `pending` works once it is `ready` — the signature doesn't cover status.
- `GET …/_files/<field>[/<file id>]?version=<name>` with the same rules, signed links, proxy mode and caching as the source; not `ready` → `404 version_unavailable` with status and reason in `details`.
- JS client: `fileUrl(id, field, { version })` returns `null` unless `ready`; the status and dimensions are read from the document (plus a helper such as `versionStatus()`), so UIs can show "processing…" or "no preview" and reserve layout space.

## 11. Security

- Pure-Go decoding removes memory-corruption bugs of C image libraries; resource attacks are bounded by the pixel check before decoding, the timeout and concurrency.
- Work runs on workers only.
- Metadata removed by default; versions never carry any.
- Covered by the file storage security review (8.8).

## 12. Documentation and tests

- Docs: "Image versions" guide (declaring, kinds, parameters and `fit` examples with pictures, statuses, downloads, client usage), functions API, `keep_metadata` and the GPS warning, worker sizing, regeneration after config changes; the workshop example's thumbnail function replaced by a declared version.
- Tests: each `fit`; no enlarging; every input format and skipped types; decompression-bomb refusal without decoding; timeout; derived concurrency under a container memory limit; statuses and reasons; replace and delete of the source; function `regenerate`/`generate`/`delete` and the writable check; fingerprint detection and the regenerate command (resume after a killed worker); metadata removal without re-compression (byte-level check that pixel data is unchanged); `keep_metadata: true`; downloads of versions in both modes; `version_unavailable`.

## 13. Issues

Milestone `11-image-versions`.

| id | Issue | Depends on |
|---|---|---|
| 11.1 | Image engine: a tested pure-Go package (decode, dimension check, orientation, fit, encode, limits); decides on WebP output | — |
| 11.2 | Declared versions in `collection.yaml`: parsing, startup checks, statuses and dimensions in documents | 8.2 |
| 11.3 | Generation on workers: jobs after upload, storage, retries, reset with the source, worker settings | 11.1, 11.2 |
| 11.4 | Downloads with `?version=`, JS client, guide and worker sizing docs, workshop example | 11.3, 8.7 |
| 11.5 | Versions from functions (`regenerate`, `generate`, `delete`, `writable`) | 11.3, 8.6 |
| 11.6 | Stale versions: fingerprints, startup report, regenerate command | 11.3, f6 |
| 11.7 | Metadata removal from originals (`keep_metadata`) | 8.2 |
| 7.35 (#159) | Umbrella: points to this design and 11.1–11.7 | — |

11.1 has no dependencies and can start at any time; 11.7 needs only file fields (8.2).

## 14. Alternatives considered

- **Declared only / functions only:** chose both on one engine.
- **Separate fields naming their source:** can't do one version per file of a `multiple` field; drift and protection problems.
- **`null` only for missing versions:** can't tell "processing" from "never"; chose statuses.
- **Per-field limits only / fixed limits:** chose operator-owned ceilings fields can lower.
- **A general transform returning bytes / byte upload into versions:** memory and vocabulary concerns; chose generate into declared versions.
- **Automatic regeneration after deploy / new uploads only:** chose detection plus on-demand regeneration.
- **Originals untouched / opt-in stripping:** chose privacy by default with `keep_metadata`.
- **C image libraries (libvips, ImageMagick):** faster and more formats, but memory-unsafe decoding and cgo; rejected.