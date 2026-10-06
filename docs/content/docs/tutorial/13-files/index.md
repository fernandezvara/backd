---
title: "13. Files on assets"
description: "Give assets a file: backd's own storage in a MinIO next to it, files: in collection.yaml, uploads, previews and download buttons, and the rules that already protect them."
weight: 330
toc: true
---

Until now an asset was a link or a note. Real shelves hold files: a PDF handbook, a picture of the office. `backd` keeps no files itself: a realm brings **its own S3-compatible storage**, and the tutorial's stack gains a MinIO to be it. Every file belongs to a field of a document and is as private as that document: reading a file is reading the asset, and every upload or removal is an update checked **before any byte is stored**.

## The storage

Fetch the stack's two files again: `compose.yaml` now starts MinIO (published on `localhost:9000`, with the bucket `backd-files` made at start and CORS for the app), and `nginx.conf` lets file uploads up to 26 MiB through to `backd`.

```sh
curl -fsSLO https://fernandezvara.github.io/backd/tutorial/compose.yaml
curl -fsSLO https://fernandezvara.github.io/backd/tutorial/nginx.conf
for f in files sha256 xhr; do   # three more files of the client the app imports
  curl -fsSL "https://fernandezvara.github.io/backd/tutorial/client/$f.js" -o "client/$f.js"
done
curl -fsSL https://fernandezvara.github.io/backd/tutorial/app/style.css -o app/style.css
docker compose up -d
```

Tell the realm where the storage is. Add to `config/shelf/realm.yaml`:

```yaml
storage:
  provider: minio
  endpoint: http://minio:9000
  public_endpoint: http://localhost:9000
  bucket: backd-files
  prefix: shelf
  access_key: secret:STORAGE_ACCESS_KEY
  secret_key: secret:STORAGE_SECRET_KEY
```

`backd` reaches MinIO at `endpoint`, inside the stack. Your **browser** can't, so links are signed for `public_endpoint`, the address it uses. The keys are realm **secrets**, never written in `config/`; set them with the operator's `$OP` (chapter 4) and ask `backd` to check the storage the way files will use it:

```sh
for pair in STORAGE_ACCESS_KEY=backd-dev STORAGE_SECRET_KEY=dev-p4ssw0rd!; do
  curl -s -X PUT "http://localhost:8080/v1/shelf/_admin/secrets/${pair%%=*}" \
    -H "Authorization: Bearer $OP" -H 'Content-Type: application/json' \
    -d "{\"value\":\"${pair#*=}\"}"                     # 204: realm scope, no database
done
docker compose restart backd
curl -s -X POST http://localhost:8080/v1/shelf/_admin/storage/check -H "Authorization: Bearer $OP" | jq .ok   # true
```

(`backd storage check` is the same from the command line. It stores a few small objects under `shelf/shelf/_check/`, tries every operation a file will need, and deletes them again.)

## Declare the file field

Files are declared in **`collection.yaml`**, not in `schema.json`: `backd` adds their schema itself (and refuses a file field in `schema.json`). The reserved `file` property of chapter 1 moves out of the schema, `kind` learns `"file"`, and `assets/collection.yaml` gains `files:`:

{{< example-file path="shelf/main/assets/collection.yaml" lines="13-28" >}}

`file` takes one document or image, up to 25 MiB, **streamed through `backd`**. Its `types` are checked against what the bytes say, never the name or the type the browser claimed. `kind` learns `"file"` in `schema.json` (and the `file` property is gone):

{{< example-file path="shelf/main/assets/schema.json" lines="6-6" >}}

```sh
docker compose restart backd
```

## Upload, replace, remove

The asset doesn't exist yet when someone picks a file for a new one, so the app uploads first and names the upload in the create, as a **pending upload**: `prepareUpload()` sends the file and answers a token, and `create` carries it as `{ upload, token }`. The file's details arrive in the same write, validated with the rest of the asset:

```js
const up = await assets.prepareUpload('file', file, { onProgress })
await assets.create({ title, kind: 'file', file: up.ref })   // up.ref is { upload, token }
```

On an asset that exists, `uploadFile()` replaces the file, and `deleteFile()` removes it. Both are updates, so both take `ifMatch` and lose to a concurrent change instead of overwriting it:

```js
await assets.uploadFile(asset.id, 'file', picked, { ifMatch: asset._meta.version })
await assets.deleteFile(asset.id, 'file', fileId, { ifMatch: asset._meta.version })
```

The app already has all of this: pick **file** in *My assets → New asset*, choose a PNG, save. Then try the refusals:

- a file over 25 MiB: `413`, answered before the file is stored;
- a Windows executable renamed `photo.png`: `415 unsupported_file_type`, because `backd` looks at the bytes.

## Previews and download buttons

The gallery and *My assets* ask for links with the list, `assets.list({ …, fileLinks: true })`: every file comes with a `url` and an `expires_at` and the card shows the image. Those links last five minutes (`presigned_ttl`), and a page can stay open for hours, so **links for what is shown at once** use `fileLinks`, and **downloads fetch a fresh link when the button is clicked**:

```js
await assets.downloadFile(asset.id, 'file', file.id)   // a fresh link, opened: the browser saves the file
```

`downloadFile()` is `fileUrl()` followed by `location.assign()`. The link makes the browser download and stay on the page. It is for browsers: elsewhere it throws and `fileUrl()` is what to use. (A plain `<a href>` to `backd`'s download route would carry no credentials, and `window.open` after an `await` is blocked as a popup: that is why the button does it this way.)

## The rules need no change

Reading an asset's file is reading the asset: `read` decides, and an asset you can't read answers `404` for its files too. An upload is an update: the `update` rule runs with `data` being the asset **as it will be**, before the first byte moves, and `changed()` lists `file`. The `published_at` lesson of chapter 5 carries over: nothing here lets a member write a field the rules refuse. Ask a curator to upload to a member's published asset (readable, not theirs): `403`, and nothing was stored.

And chapter 2's `on_owner_delete: delete` already covers files: erasing a member queues their assets' files for deletion, and a worker removes the objects.

## Fetch the chapter's files

{{< tutorial-files "realm.yaml main/assets/schema.json main/assets/collection.yaml main/assets/rules.yaml" >}}

## You should see

- `POST /v1/shelf/_admin/storage/check` answers `"ok": true`, with the signed SHA-256 verified.
- *My assets → New asset → kind: file* with a PNG saves a draft whose card shows the picture; the **⬇** button saves the file.
- A 26 MiB file answers `413`, a renamed executable `415 unsupported_file_type`, and a replace with a stale version `412`.
- A curator's upload to a member's published asset answers `403`.
- `curl http://localhost:9000/backd-files/` is refused: the bucket is private, and only links `backd` signed work.

Next: chapter 14 — big files go straight to the bucket, and a function makes thumbnails.
