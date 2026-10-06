---
title: "14. Big files and thumbnails"
description: "Direct uploads with a progress bar, several attachments per asset, and a thumbnail function that writes the one field the rules refuse to clients."
weight: 340
toc: true
---

A 25 MiB limit and a trip through `backd` suit a handbook, not a conference recording. This chapter adds **attachments**: several files, up to a gigabyte each, that the browser sends **straight to the bucket**. Then a function makes a small picture of each image, and the rules keep clients from writing it.

## Attachments, straight to the storage

`attachments` was declared with `files:` in the last chapter. What makes it different is one line, `upload: direct`:

{{< example-file path="shelf/main/assets/collection.yaml" lines="18-23" >}}

The browser asks `backd` to start the upload (declaring the file's name, size, type and SHA-256), receives a **signed link** with those signed in, sends the bytes to MinIO, and tells `backd` to complete: `backd` checks the size, the checksum **the storage computed** and the type from the first bytes, and attaches the file. Anything that doesn't match deletes the object. The client does all of it:

```js
await assets.uploadFile(asset.id, 'attachments', file, { onProgress: ({ loaded, total }) => bar.set(loaded / total) })
```

The checksum is computed incrementally while the file is read, so a large file isn't held in memory, and `onProgress` reports the bytes the browser has sent (through `XMLHttpRequest`). The bucket must allow the app's origin: the stack's MinIO already does (`MINIO_API_CORS_ALLOW_ORIGIN`). If you ever see `upload_failed`, it is nearly always CORS.

Try it: *My assets → New asset → kind: file*, pick two or three big files as **Attachments**, and watch the bar. Add more to an existing asset with **Add an attachment**. The sixth is refused: `409 too_many_files`, answered before any byte moves.

{{< hint style="tip" title="Best practice" >}}
`max_files`, `max_size` and `types` are limits `backd` enforces, but a bucket that accepts direct uploads from browsers needs CORS **for your app's origin only**. In production, set it per provider ([provider pages](../../files/)) and let `backd storage check` read it back.
{{< /hint >}}

## A thumbnail, written by a function

A gallery of full-size images is slow. `thumbnail` makes a small PNG of an asset's image and stores it in the `thumbnail` field. Nobody but this function should write that field, or an owner could put anything there. Start with the function, then close the hole.

The function has three files, like every function (chapter 5):

{{< example-file path="shelf/main/_functions/thumbnail/function.yaml" >}}

It is **async** (the app doesn't wait for it), `admin: true` (so `ctx.admin.db` can write the protected field) and has `retry:` for a busy storage. The logic is in `lib/thumbnail.ts`, apart from the image library, so it is testable without it:

{{< example-file path="shelf/main/_functions/lib/thumbnail.ts" >}}

Read what it does with the files API: `files(id, field).get()` reads the picture, `put()` stores the result, and **two identities do two jobs**. `ctx.db` is the caller: the function first reads the asset as them, so a member can only ask for a thumbnail of what they can read. `ctx.admin.db` is the function: it writes the thumbnail. The index wires in the library:

{{< example-file path="shelf/main/_functions/thumbnail/index.ts" >}}

(The packages are downloaded and pinned when `functions-build` runs, never at run time. They are pure JavaScript on purpose: a function has no permission to read files, so a library that loads WebAssembly or native code from disk would fail to start.) Fetch the files, build, restart:

{{< tutorial-files "main/_functions/lib/types.ts main/_functions/lib/thumbnail.ts main/_functions/thumbnail/function.yaml main/_functions/thumbnail/index.ts main/_functions/thumbnail/index.test.ts main/_functions/thumbnail/input.schema.json" >}}

```sh
docker compose run --rm functions-build && docker compose restart backd
```

The app starts the job after an image upload and refreshes when it finishes: upload a PNG and the card swaps its picture for the thumbnail. The function's logic is tested with the fake `ctx` of chapter 5, which has the files API too: `deno test config/shelf/main/_functions/thumbnail/`.

## The hole, and closing it

Until now **nothing stops a member from writing the thumbnail themselves**. Prove it with a fake one (`$TOKEN` is a member's session; use any small PNG):

```sh
ASSET=<an asset id of yours>
curl -s -X POST "http://localhost:8080/v1/shelf/main/assets/$ASSET/_files/thumbnail?name=fake.png" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: image/png' --data-binary @tiny.png   # 201: accepted
```

The rules from chapter 5 refused `published_at` the same way, and they refuse this one the same way. Rules for files **are** the rules for the asset: an upload is an update, and `changed()` lists the file field. On a create there is no `changed()`, so the rule asks the field to be absent. Edit `assets/rules.yaml`: `create` and `update` each gain a line (the rest stays as chapter 5 left it):

```yaml
create: >
  user != nil && data.published_at == nil
  && data.thumbnail == nil
update: >
  user != nil && document._meta.owner == user.id
  && !('published_at' in changed())
  && !('thumbnail' in changed())
```

```sh
docker compose restart backd
```

Repeat the `curl`: **`403`**, and nothing was stored (the rule runs before the first byte). A member naming a pending upload in the thumbnail field of a create gets `403` too, and the thumbnail function keeps working: it writes through `ctx.admin.db`, which skips the rules.

## You should see

- An attachment of a few hundred MiB uploads with a progress bar, straight to MinIO (watch `docker compose logs minio`: the PUT arrives there, not at `backd`).
- Five attachments fit; the sixth answers `409 too_many_files`.
- After a PNG upload, the gallery shows a thumbnail once the job finishes (*Admin → Run by hand* can't start it, but `docker compose logs backd` shows the job).
- A client upload to `_files/thumbnail` answers `403` once the rule is in place, and `201` before.
- `deno test` passes for `thumbnail`.

Next: chapter 15 — sharing files, and counting downloads.
