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
// index.ts: the function, with an image library
import { Image } from "npm:imagescript";
import { makeThumbnailer } from "./thumbnail.js";

export default makeThumbnailer(async (bytes: Uint8Array, width: number) => {
  const image = await Image.decode(bytes);
  return await image.resize(width, Image.RESIZE_AUTO).encode();
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

## Testing

[`@backd/functions-testing`](../testing/) fakes `files(id, field)` over its in-memory store: `put`, `get`, `delete` and `link` with the same names and results. Declare a collection's file fields with `store.declareFiles(database, collection, { receipts: { multiple: true, max_files: 3, max_size: 1000, types: ["image/*"] } })` and the fake enforces them (`too_many_files`, `payload_too_large`, `unsupported_file_type`); `store.fileBytes(fileId)` returns what was stored. Like the rest of the fake it evaluates no access rules, and it trusts the `type` it is given instead of detecting it.

## Good to know

- **Limits are the field's.** `max_size`, `types` and `max_files` apply, and a function's upload is not capped by `MAX_BODY_BYTES` (but is by `BACKD_MAX_UPLOAD_BYTES`). A function's memory is its own `memory` limit: put and get through `bytes()` hold the whole file, so stream large ones.
- **Direct uploads are not for functions.** A field with `upload: direct` takes its bytes straight to the bucket from a client; a function can't `put` to it.
