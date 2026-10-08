---
title: "File fields"
description: "files: in collection.yaml: which fields of a document carry files, their limits, and what backd keeps in them."
icon: "attach_file"
weight: 576
toc: true
---

A collection says which fields of its documents carry files under `files:` in its [`collection.yaml`](../../configuration/config-dir/#collectionyaml). The realm needs a [`storage:`](../storage/) section.

```yaml
files:
  avatar:
    max_size: 2MiB
    types: [image/png, image/jpeg, image/webp]
  receipts:
    multiple: true
    max_files: 5
    max_size: 10MiB
    types: [application/pdf, image/*]
  manual:
    max_size: 20MiB
    download: proxy
    cache: 1h
```

| Key | Default | Meaning |
|---|---|---|
| `max_size` | required | The largest one file may be (`512KiB`, `10MiB`, `1GiB`). Checked while the bytes stream in |
| `types` | any | Allowed content types, `image/png` or a family such as `image/*`. Checked against the type **detected from the file's content**, never the one the client claims. A family never admits SVG, HTML or XML: name them to accept them |
| `multiple` | `false` | The field is a list of files instead of one |
| `max_files` | `10` | With `multiple`: how many files the field may hold (1 to 1000) |
| `upload` | `proxy` | `proxy` streams the bytes through backd; `direct` has the client send them straight to the bucket with a signed link, which backd verifies afterwards ([Direct uploads](../transfers/#direct-uploads)). `max_size` is at most 5GiB for `direct` |
| `download` | the realm's | `presigned` redirects to a short-lived link of the storage; `proxy` streams the file through backd |
| `presigned_ttl` | the realm's | How long this field's links last (at most 7 days) |
| `cache` | none | With `download: proxy`: how long shared caches may keep the file, only when anyone may read the document |
| `max_pixels` | the instance's | For images: the most pixels (width × height) the field gets [versions](#image-versions) for. It can only lower `BACKD_IMAGE_MAX_PIXELS`; a higher value stops startup |
| `versions` | none | Resized copies of an image, declared by name: see [Image versions](#image-versions) |

A field name is lower-case letters, digits and underscores. **A file field is declared only here:** one that is also in `schema.json` is a startup error, because backd adds its schema itself, to the validators in MongoDB, to the [rules](../rules/) and to the queries.

## What a file field holds

backd owns it. Clients can't write it: a `POST`, `PUT`, `PATCH` or batch operation drops file fields from the body, and a `PUT` keeps the files the document has. Files change only through [uploads and removals](../transfers/), or by naming a [pending upload](../transfers/#creating-a-document-with-its-files) in a write. A single field holds one object, a `multiple` one a list of them:

```json
{
  "id": "fl_8f3k2n5x0q7w1a9b4c6d",
  "name": "receipt.pdf",
  "size": 48213,
  "type": "application/pdf",
  "sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
  "uploaded_at": "2026-10-06T10:15:00.000Z"
}
```

`type` is the detected content type, `sha256` the digest of the bytes backd stored. Names are cleaned, never rejected: Unicode is normalized (NFC), path and control characters are dropped, the name is cut at 255 bytes and an empty one becomes `file`.

Objects are stored under `<prefix>/<realm>/<database>/<collection>/<file id>` and never move. The name is not part of the key, so renaming or sharing names never touches storage.

## Image versions

A field can declare **versions**: resized copies of the images it holds, such as a thumbnail and a preview, named in `collection.yaml` and recorded in each file's details, so an app always knows where to look.

```yaml
files:
  photo:
    max_size: 25MiB
    types: [image/png, image/jpeg, image/webp]
    max_pixels: 24000000
    versions:
      thumb:   { max_width: 256,  max_height: 256,  fit: cover,   format: jpeg, quality: 80 }
      preview: { max_width: 1280, max_height: 1280, fit: contain, format: jpeg, quality: 85, writable: true }
      mark:    { writable: true }
```

| Key | Default | Meaning |
|---|---|---|
| `max_width`, `max_height` | none | The box in pixels (1 to 16384). A version that backd makes needs at least one |
| `fit` | `contain` | `contain` fits the image inside the box keeping its proportions; `cover` fills the box and crops the excess, centered (needs both sides); `stretch` fills the box and ignores the proportions |
| `format` | the source's family | `jpeg` or `png`. JPEG stays JPEG, PNG and GIF become PNG, WebP becomes JPEG unless it has transparency. WebP can't be written |
| `quality` | `82` | 1 to 100, for `jpeg` |
| `upscale` | `false` | Enlarge an image smaller than the box. By default a smaller image is never enlarged |
| `writable` | `false` | Functions may make this version. Without a box, **only** functions make it |

A version needs a box (a worker makes it), `writable: true` (a function makes it), or both (a worker makes it and a function may redo it). Anything else, a `fit` that doesn't exist, a `quality` out of range or `format: webp` stops startup naming the version. Version names are lower-case letters, digits, hyphens and underscores.

Backd reads JPEG, PNG, GIF (the first frame) and WebP; any other file, or a file that isn't an image, is **skipped** and never an upload error. The pixel count is read from the file's header, so an image over the limit is refused without being decoded.

What a document records, next to the file's other details:

```json
{
  "id": "fl_8f3k2n5x0q7w1a9b4c6d", "name": "photo.jpg", "size": 3481220, "type": "image/jpeg",
  "width": 4032, "height": 3024,
  "versions": {
    "thumb": { "status": "pending" },
    "preview": { "status": "pending" },
    "mark": { "status": "empty" }
  }
}
```

`width` and `height` are the image's size as a viewer sees it (the EXIF orientation applied), and they are recorded for every image the engine can read, with or without versions. Each version has a `status`:

| Status | Meaning |
|---|---|
| `pending` | A worker will make it |
| `ready` | Made; with its `id`, `size`, `type`, `width`, `height`, `params` and `generated_at` |
| `empty` | Only functions make it, and none has yet |
| `skipped` | The file isn't an image backd reads. `reason`: `not_an_image` or `unsupported_format` |
| `failed` | `reason`: `too_large` (over the pixel limit) or `decode_error` |

Like the rest of a file's details, clients can't write them.

### How versions are made

When an upload becomes part of a document, one job per file is queued (origin `backd:image.versions`, visible in the [jobs](../../functions/jobs/) list and counted by the `image` job kind in the [metrics](../../operations/metrics/)). A **worker** reads the original, decodes it **once**, makes every pending version, stores each beside the original under `<file key>/<version name>` and records its details in the document:

```json
"thumb": {
  "status": "ready", "id": "fv_d3a1k2n5x0q7w1a9b4c6", "size": 18342, "type": "image/jpeg",
  "width": 256, "height": 256,
  "params": { "max_width": 256, "max_height": 256, "fit": "cover", "quality": 80, "format": "jpeg" },
  "fingerprint": "a91c4be2f10d7733"
}
```

- **Metadata is never copied:** a version has none (no EXIF, no GPS), and its pixels are upright.
- **Writing the details doesn't change the document's `_meta.version`**, so a client editing the document is never told it conflicts because a version finished.
- **A failure is recorded, not raised:** an unreadable image fails its versions with `decode_error`, one over the limit with `too_large`, one that takes too long with `timeout`. A storage or database error is retried with a growing wait, five times, and then fails the pending versions with `storage_error`.
- **A worker that dies mid-job** loses nothing: its lease lapses, another worker takes the job and repeats it. Running a job twice is harmless.
- **Replacing or deleting the original** queues its versions' objects for deletion too, and the new file starts with its own versions. Deleting the document, or erasing a user, does the same.
- **Versions count in the [usage](../maintenance/#usage)** of the realm and of the document's owner, though they are never refused by a [quota](../storage/#quotas).
- `backd storage reconcile` keeps the version objects a document records and reports the rest of a file's `<name>` objects as orphans.

### Sizing the worker

Decoding takes memory: about **4 bytes per pixel** of the image, plus the scaled copies. A 40-megapixel photograph needs about 160 MB while it is decoded.

| `BACKD_IMAGE_MAX_PIXELS` | Per image | Two at once |
|---|---|---|
| 12 000 000 (a phone photo) | ~50 MB | ~100 MB |
| 40 000 000 (the default) | ~160 MB | ~320 MB |
| 100 000 000 | ~400 MB | ~800 MB |

Give the worker's container at least twice what the table says for the concurrency you want, since the original is also read and the runtime needs room. By default the concurrency is derived from the container's CPU and memory limits (half the CPUs, and no more than fit in half the memory), and logged at startup as `image versions`; set `BACKD_IMAGE_CONCURRENCY` to choose it. If the worker is undersized, the container is killed for memory, another worker retries the job when the lease lapses, and an image that keeps killing it can be kept out with a lower `BACKD_IMAGE_MAX_PIXELS` or a field's `max_pixels`.

### Showing and downloading versions

`GET …/_files/{field}/{file id}?version=thumb` serves a version like its original ([Downloading](../transfers/#downloading)), and `?file_links=true` links every `ready` version with the document. In the [JavaScript client](../../clients/js/#files):

```js
const doc = await photos.get(id, { fileLinks: true })
const thumb = doc.photo.versions.thumb          // { status, width, height, url, … }
img.src = thumb.url                              // only a ready version has a link

versionStatus(doc.photo, 'thumb')               // { status: 'pending', ready: false, … }: "processing…", and room to reserve
await photos.fileUrl(id, 'photo', doc.photo.id, { version: 'thumb' })   // a link, or null while it isn't ready
```

Read `width` and `height` from the document to reserve room for a picture before it loads, and `status` to say "processing…" or "no preview".

{{< hint style="note" >}}
The functions' API for versions and regenerating them after a configuration change are planned.
{{< /hint >}}

## Changing `files:`

- **Tighter limits** (`max_size`, `types`, `max_files`) apply to the next uploads. Files already stored are not touched or re-checked.
- **Removing a field** from `files:` makes what documents hold in it inert: it is no longer served, and the objects stay in the bucket until removed by hand.
- **Rules are checked at startup** with the injected fields known, like any other field.
