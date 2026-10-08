---
title: "14. Big files and thumbnails"
description: "Direct uploads with a progress bar, several attachments per asset, and a thumbnail declared as a version that a worker makes."
weight: 340
toc: true
---

A 25 MiB limit and a trip through `backd` suit a handbook, not a conference recording. This chapter adds **attachments**: several files, up to a gigabyte each, that the browser sends **straight to the bucket**. Then a worker makes a small picture of each image, declared in one line.

## Attachments, straight to the storage

`attachments` was declared with `files:` in the last chapter. What makes it different is one line, `upload: direct`:

{{< example-file path="shelf/main/assets/collection.yaml" lines="55-60" >}}

The browser asks `backd` to start the upload (declaring the file's name, size, type and SHA-256), receives a **signed link** with those signed in, sends the bytes to MinIO, and tells `backd` to complete: `backd` checks the size, the checksum **the storage computed** and the type from the first bytes, and attaches the file. Anything that doesn't match deletes the object. The client does all of it:

```js
await assets.uploadFile(asset.id, 'attachments', file, { onProgress: ({ loaded, total }) => bar.set(loaded / total) })
```

The checksum is computed incrementally while the file is read, so a large file isn't held in memory, and `onProgress` reports the bytes the browser has sent (through `XMLHttpRequest`). The bucket must allow the app's origin: the stack's MinIO already does (`MINIO_API_CORS_ALLOW_ORIGIN`). If you ever see `upload_failed`, it is nearly always CORS.

Try it: *My assets → New asset → kind: file*, pick two or three big files as **Attachments**, and watch the bar. Add more to an existing asset with **Add an attachment**. The sixth is refused: `409 too_many_files`, answered before any byte moves.

{{< hint style="tip" title="Best practice" >}}
`max_files`, `max_size` and `types` are limits `backd` enforces, but a bucket that accepts direct uploads from browsers needs CORS **for your app's origin only**. In production, set it per provider ([provider pages](../../files/)) and let `backd storage check` read it back.
{{< /hint >}}

## A thumbnail, declared

A gallery of full-size images is slow, and a small copy of each picture fixes it. There is nothing to write: the `file` field **declares** a version, and a worker makes it.

{{< example-file path="shelf/main/assets/collection.yaml" lines="47-54" >}}

`thumb` fits the picture in a 256 × 256 box, cropping the excess (`cover`), and keeps the picture's format (a PNG stays a PNG). When an upload becomes part of an asset, `backd` queues a job and a **worker** (the stack already runs one) reads the original, makes the copy and stores it beside it. The asset records what happened, next to the file's other details:

```json
"file": {
  "id": "fl_8f3k2n5x0q7w1a9b4c6d", "name": "office.png", "type": "image/png", "size": 1203442,
  "width": 800, "height": 600,
  "versions": { "thumb": { "status": "ready", "type": "image/png", "width": 256, "height": 256, "size": 41022 } }
}
```

Until the worker is done the status is `pending`, so the app can say so; a PDF or a text file has no thumbnail, and its version is `skipped` rather than an error. The tutorial's `backd` runs its worker inside (`--with-worker`), so it needs nothing else. Fetch the finished `collection.yaml` and restart:

{{< tutorial-files "main/assets/collection.yaml" >}}

```sh
docker compose restart backd
```

The app asks for links with `fileLinks: true`, which also links every `ready` version, so a card shows the thumbnail when there is one and the picture when there isn't:

```js
previewUrl(asset) {
  return asset.file?.versions?.thumb?.url ?? (this.isImage(asset.file) ? asset.file.url : null)
}
```

After an upload the app looks again every second and a half, for a few seconds, until `versionStatus(asset.file, 'thumb')` says the worker is done. To link a version by hand, `assets.fileUrl(id, 'file', fileId, { version: 'thumb' })` answers the link, or `null` while the version isn't ready. The API says the same with `404 version_unavailable`, carrying the version's status and reason.

Try it: *My assets → New asset → kind: file*, pick a PNG or JPEG, and watch the card swap its picture for the thumbnail. The worker's memory and how many pictures it handles at once are covered in [Image versions](../../files/file-fields/#image-versions).

{{< hint style="tip" title="Already done for you" >}}
A version has no EXIF and no GPS position, whatever the original carries, and its pixels are upright. A thumbnail is safe to show to people who can't see the original.
{{< /hint >}}

The details a worker records (`width`, `height`, `versions`) are `backd`'s: a client that tries to write them has them dropped, so there is no rule to add for them. What a function writes into a field of its own is chapter 15.

## You should see

- An attachment of a few hundred MiB uploads with a progress bar, straight to MinIO (watch `docker compose logs minio`: the PUT arrives there, not at `backd`).
- Five attachments fit; the sixth answers `409 too_many_files`.
- After a PNG upload, the card shows the thumbnail within a few seconds (`docker compose logs backd` shows the `image versions made` line).
- A text file's `versions.thumb.status` is `skipped`; asking for its thumbnail answers `404 version_unavailable`.

Next: chapter 15 — sharing files, and counting downloads.
