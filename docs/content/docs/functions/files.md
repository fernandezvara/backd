---
title: "Files in functions"
description: "Read, write, remove and link a document's files from a function with files(id, field), as the caller or as the function, and two recipes: a thumbnail and a counted download."
icon: "attach_file"
weight: 566
toc: true
---

A function reaches the files of a document through the same client it uses for data: `files(id, field)` on a collection of `ctx.db` or `ctx.admin.db`. It makes the same requests as any client, to the [`_files` routes](../../files/transfers/), so the same limits, type detection, journal and usage totals apply.

```js
const photos = ctx.db("main").collection("people").files(personId, "photo");

const doc  = await photos.put(bytes, { name: "ada.png", type: "image/png" }); // the updated document
const file = await photos.get();              // { file, response, bytes(), text() }
const link = await photos.link();             // { url, expires_at }
await photos.delete();                        // the field's files; delete(fileId) removes one
```

| | |
|---|---|
| `put(data, { name, type, ifMatch })` | Stores a file (a string, bytes, a `Blob` or a stream): it replaces a single field's file and is added to a `multiple` field's. Resolves with the document, which now holds the file's details. `type` is a hint: backd detects the real one from the content |
| `get(fileId?)` | Downloads a file. Without `fileId`, the one a single field holds; a field with several needs one. The bytes come through backd, even where downloads are redirects for everyone else, because a function can't reach the bucket |
| `link(fileId?)` | `{ url, expires_at }`, exactly like `?link=json`: for the app to open. A link of the storage is for the bucket; a [backd link](../../files/transfers/#links-for-apps) needs `BACKD_URL`, so it can name backd's public address |
| `delete(fileId?, { ifMatch })` | Removes one file, or every file of the field. Resolves with the document |

## As the caller, or as the function

`ctx.db` acts as the caller: `put` and `delete` are asked of the document's `update` rule (with `data` and `changed()` as for an upload), `get` and `link` of the `read` rule, and an upload is recorded as theirs. A document the caller can't read is a `NotFoundError`.

`ctx.admin.db` (a function with `admin: true`, or a scheduled run) skips the rules, which is how a function writes a field the rules reserve for it. A rule such as `update: … && !('thumbnail' in changed())` refuses a client that tries to set the thumbnail and lets the function set it. Both are listed in [Rules for files](../../files/rules/#reserving-a-field-for-functions). Review a function that uses `ctx.admin.db` as you would any privileged code.

## A thumbnail in a reserved field

The function reads the original with `get`, resizes it, and writes the result to `thumbnail`, which the rules refuse to clients. Only the resizing needs a package, so it is the one thing passed in, and the rest is tested without it.

```js
// thumbnail.js
export function makeThumbnailer(resize) {
  return async (ctx) => {
    const { person } = ctx.input ?? {};
    if (typeof person !== "string") throw ctx.error(400, "person_required", 'say which person: { "person": "<id>" }');

    const people = ctx.admin.db("main").collection("people");
    const photo = await people.files(person, "photo").get();      // NotFoundError when there is none
    const small = await resize(await photo.bytes(), 128);

    // A single field: the new thumbnail replaces the old one, whose object backd deletes.
    const doc = await people.files(person, "thumbnail").put(small, { name: "thumbnail.png", type: "image/png" });
    return { thumbnail: doc.thumbnail.id, size: doc.thumbnail.size };
  };
}
```

```ts
// index.ts: the function, with image libraries (pure JavaScript: a function can't read files,
// so a library that loads WebAssembly or native code from disk can't start)
import UPNG from "npm:upng-js@2.1.0";
import { makeThumbnailer } from "./thumbnail.js";

export default makeThumbnailer(async (bytes: Uint8Array, width: number) => {
  // decode with UPNG (PNG) or jpeg-js (JPEG), average the pixels down to `width`, encode with UPNG.encode
  // (for a plain thumbnail you don't need a function: declare a [version](../../files/file-fields/#image-versions) and a worker makes it)
});
```

```yaml
# function.yaml
admin: true
```

## A download that is counted

A function is called with `POST` and answers JSON, so it can't stream a file: it returns the link and the app opens it. This one counts the download, then returns the link. It reads as the caller, so the `read` rule decides who gets a link at all, and it keeps the count with `ctx.admin.db`:

```js
export default async function download(ctx) {
  const { release, file } = ctx.input ?? {};
  if (typeof release !== "string" || typeof file !== "string") throw ctx.error(400, "bad_input", 'give { "release", "file" }');

  const releases = ctx.db("main").collection("releases");
  const link = await releases.files(release, "assets").link(file);  // NotFoundError if they may not read it

  const counters = ctx.admin.db("main").collection("releases");
  const doc = await counters.get(release);
  await counters.patch(release, { downloads: (doc.downloads ?? 0) + 1 }, { ifMatch: doc._meta.version });

  return { url: link.url, expires_at: link.expires_at };
}
```

Add the same rule trick to `downloads` (`!('downloads' in changed())`) so a client can't set its own count. Both recipes are exercised by tests in the repository (`clients/functions-testing/examples/files/`), with the fake `ctx` below.

## Versions of an image

A picture's declared [versions](../../files/file-fields/#image-versions) (a thumbnail, a preview) are made by workers after an upload. A function can make one again, make it with other parameters, or drop it, through the file's `version(name)`:

```js
const photo = ctx.db("app").collection("assets").files(id, "photo");
const preview = photo.version("preview");   // a second argument names the file of a `multiple` field

await preview.regenerate();                                              // the parameters collection.yaml declares
const made = await preview.generate({ max_width: 900, fit: "cover", format: "png" }); // other parameters: a `writable` version only
// made = { status: "ready", id: "fv_…", width: 900, height: 900, custom: true, … }
await preview.delete();                                                  // back to pending (a worker remakes it) or empty
```

| | |
|---|---|
| `regenerate()` | Makes the version again with its declared parameters, and replaces the old copy. A version only functions make (no `max_width` or `max_height`) has nothing to remake from: `400` |
| `generate(params)` | Makes it with `max_width`, `max_height`, `fit`, `quality`, `format` and `upscale` (named and checked as in `collection.yaml`; `400` otherwise). Only a version declared `writable: true` allows it: `403 version_not_writable`. The state records `custom: true`, so it can be told from a declared one |
| `delete()` | Drops the copy. The version is `pending` again when it has parameters of its own (a worker makes it, so it is not gone for long) and `empty` when only functions make it; the object is deleted |

Both of the first two **wait for a worker** to make the version and resolve with its new state, so the function's own `timeout` (and a running worker) matter; with no worker running they fail with `504 version_timeout`. An image that can't be made fails with a `422` whose `code` is the reason: `not_an_image`, `unsupported_format`, `too_large`, `decode_error` or `timeout`. The version that was there is kept. A function gets these as a `BackdError` (`e.status`, `e.code`); let it end the function, or `throw ctx.error(422, e.code, …)` to hand the reason to the caller.

The identity applies as for any file operation: under `ctx.db` the document's `update` rule is asked (with the document as it is, so `changed()` is empty), and `ctx.admin.db` skips it. Changing a version never changes the document's `_meta.version`. There is no way to upload bytes into a version: it is always made from the original.

A function-only version is how a function keeps a result of its own next to the original, such as a watermarked copy: declare `watermarked: { writable: true }`, and `generate` it with the box you want when the function runs.

## Testing

[`@backd/functions-testing`](../testing/) fakes `files(id, field)` over its in-memory store: `put`, `get`, `delete` and `link` with the same names and results. Declare a collection's file fields with `store.declareFiles(database, collection, { receipts: { multiple: true, max_files: 3, max_size: 1000, types: ["image/*"] } })` and the fake enforces them (`too_many_files`, `payload_too_large`, `unsupported_file_type`); `store.fileBytes(fileId)` returns what was stored. Declare `versions` in the field (`photo: { versions: { thumb: { max_width: 10, max_height: 10 }, mark: { writable: true } } }`) and a file starts with its versions `pending`, `empty` or `skipped`, and `version(name).regenerate()`, `.generate(params)` and `.delete()` work with backd's errors (`version_not_writable`, `not_an_image`, validation). The fake makes no pictures: a made version is `ready` with the size its box gives (read from a PNG's header; other pictures have none). Like the rest of the fake it evaluates no access rules, and it trusts the `type` it is given instead of detecting it.

## Good to know

- **Limits are the field's.** `max_size`, `types` and `max_files` apply, and a function's upload is not capped by `MAX_BODY_BYTES` (but is by `BACKD_MAX_UPLOAD_BYTES`). A function's memory is its own `memory` limit: put and get through `bytes()` hold the whole file, so stream large ones.
- **Direct uploads are not for functions.** A field with `upload: direct` takes its bytes straight to the bucket from a client; a function can't `put` to it.
