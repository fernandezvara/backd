---
title: "Public files"
description: "Serve images that are not private straight from a bucket or a CDN: the second storage, the public field, the permanent address in the document, JPEG and PNG only, and what stays your responsibility."
icon: "image"
weight: 579
toc: true
---

Every other file in `backd` is private to its document: it is reached through the document's rules and a signed link that expires. That is the right default, and the wrong tool for a gallery, product pictures or an avatar people chose to show: signing every image, and asking `backd` for it, costs a request and a cache miss for something anyone may see.

A **public field** keeps its files somewhere else, in a second bucket that anyone can read, and puts the **permanent address** of each file in the document. The page can use that address the moment the document arrives, in an `<img>`, with no signing and no call to `backd`.

```json
{
  "id": "…",
  "title": "Sunset",
  "photo": {
    "id": "fl_8f3k2n5x0q7w1a9b4c6d",
    "name": "Sunset over the bay.png",
    "key_name": "Sunset over the bay.png",
    "public": true,
    "url": "https://cdn.example.com/prod/shop/main/gallery/fl_8f3k2n5x0q7w1a9b4c6d/Sunset%20over%20the%20bay.png",
    "type": "image/png", "size": 48213, "sha256": "…", "uploaded_at": "2026-10-28T10:15:00.000Z",
    "versions": { "thumb": { "status": "ready", "url": "https://cdn.example.com/…/thumb/Sunset%20over%20the%20bay.png" } }
  }
}
```

{{< hint style="warning" >}}
**Public means anyone with the address can read the file**, with no rule and no sign-in, and a CDN may keep serving it after you delete it. Only put in a public field what you would put on a web page.
{{< /hint >}}

## Setting it up

You need three things, in this order.

**1. A bucket that anyone can read, and nobody can list.** Create a bucket **other than** the one in `storage:` and let anonymous callers read its objects (`s3:GetObject`) but not list it. On MinIO that is a policy with one statement ([the development stack's](../../../operate/providers/storage/minio/#development) is in the repository); on S3, a bucket policy; on R2, a public bucket or a custom domain. Often a CDN sits in front of it, and the CDN's address is what browsers use. Give `backd` a key that can only write to this bucket and its prefix, and keep it different from the private storage's key.

**2. `public_storage:` in `realm.yaml`**, next to (or instead of) `storage:`. Its `url` is where browsers read the files; `backd` writes to `endpoint` and **never connects to `url`**. The keys are realm secrets with names of their own. [All the settings](../../../operate/providers/storage/#public-storage).

```yaml
public_storage:
  provider: r2
  endpoint: https://<account id>.r2.cloudflarestorage.com
  bucket: acme-public
  prefix: prod
  access_key: secret:PUBLIC_STORAGE_ACCESS_KEY
  secret_key: secret:PUBLIC_STORAGE_SECRET_KEY
  url: https://cdn.example.com
```

**3. `public: true` on the file field**, in `collection.yaml`. This is the workshop example's gallery:

{{< example-file path="workshop/main/gallery/collection.yaml" >}}

Then check it. `backd storage check` tests the public storage's keys and the headers it stores (see [Connecting storage](../../../operate/providers/storage/#checking-it)); because `backd` never connects to the public address, it leaves one small image in the bucket and **prints the address to open**. Open it from a browser, without credentials: if it shows, the bucket (or the CDN) serves it. Also confirm that listing the bucket is refused. Do this before production.

## Uploading

A public field takes uploads through `backd` (never `upload: direct`), like a private one, and the same [rules](../rules/) decide who may upload, replace or remove. Only **JPEG and PNG** are accepted, detected from the bytes, and the file is **always cleaned of its metadata** (the GPS position of a photograph, the camera, comments) before it is published.

The address ends with the file's name, so the name matters:

- It is cleaned more strictly than other names: letters, digits and `. - _ ( ) +` and spaces are kept, anything else becomes `_`, at most 120 bytes.
- It has to end with `.jpg`, `.jpeg` or `.png` and **agree with the content**. A PNG called `photo.jpg` is refused with `400 file_extension_mismatch` and never renamed; a name with nothing usable is `400 invalid_file_name`. Any other type is `415 unsupported_file_type`.
- The address also holds the file's unique id, so two files called `photo.png` never collide and a replaced file has a new address.

With the JavaScript client, `docs.uploadFile(id, 'photo', file)` sends the name of the `File`; with the API, give `?name=` or a `Content-Disposition` filename.

## Reading

Every read of the document carries the address (`photo.url`), computed from the file's recorded key and the public storage's `url`. A [version](../file-fields/#image-versions) has its own `url` once it is `ready`. The clients read it from the details, with no request:

```js
import { publicUrl } from 'backd-js'

const { items } = await db.collection('gallery').list()
img.src = publicUrl(items[0].photo, 'thumb') ?? publicUrl(items[0].photo)  // the thumbnail once it is ready
```

The workshop example's gallery panel (page 7 of the tour, `make example`) does exactly this: it lists the gallery without signing in and shows the pictures straight from the bucket. Its realm declares the public storage like this:

{{< example-file path="workshop/realm.yaml" lines="63-70" >}}

## Caching and changing things

- An original is cached for a year (`Cache-Control: public, max-age=31536000, immutable`): a replacement is a new file with a new address, so what an address serves never changes. A version is rewritten when it is regenerated, so it is cached for a day.
- The document's `ETag` includes the public storage's `url`, so when you move to another CDN domain clients refetch and get the new addresses. Nothing stored changes.
- **Changing a field's `public` setting moves nothing.** A file stays where it was stored, with the address it has; the setting only decides where new uploads go. If you make a public field private, its old files stay public (and keep their `url`) until you remove them. There is no migration yet.
- **Renaming** the realm, a database or a collection that holds public files changes every address (the names are in them): don't, once files exist.
- Public files are included in a user's [personal data export](../../auth/personal-data/) and in [realm exports and imports](../../../operate/administration/export-import/moving-an-app/), and an import checks them like an upload before publishing them. Erasure removes them from the public bucket like any file.

## What stays yours

- Making the bucket readable by everyone and unlistable, and testing it: `backd` can't see the public address.
- A CDN's cache: a deleted file may be served until it expires. Purge it, or review the bucket, when something must go.
- Keeping the public bucket's key different from the private one, and the bucket empty of anything private: `backd` refuses a public storage that is the same bucket as `storage:`, but it can't know what else you put in it.
- CORS on the public bucket, if pages fetch the images with scripts or draw them on a canvas (a plain `<img>` needs none), and a CDN domain separate from your application's, so cookies and storage stay apart.
- Bandwidth: reads at `url` are billed to the bucket or the CDN, not served by `backd`.
