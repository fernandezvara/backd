---
title: "Uploads and downloads"
description: "Upload a file into a document, download it, remove it, and give an app links to show files."
icon: "swap_vert"
weight: 577
toc: true
---

Every file operation is an operation on a document and follows its [access rules](../rules/): the `update` rule for uploads and removals, the `read` rule for downloads. A document the caller can't read answers `404`, never revealing its files. [API keys](../../auth/api-keys/) and the [admin data route](../../api/documents/) skip rules, as everywhere.

| Operation | Request |
|---|---|
| Upload | `POST /v1/{realm}/{database}/{collection}/{id}/_files/{field}` |
| Download | `GET …/{id}/_files/{field}/{file id}` |
| Remove one | `DELETE …/{id}/_files/{field}/{file id}` |
| Clear the field | `DELETE …/{id}/_files/{field}` |

The same four exist under `/v1/{realm}/_admin/data/…`. There is no `PUT`: a file is replaced by uploading another.

## Uploading

The body is the file's bytes, with its `Content-Type`:

```sh
curl -X POST "$API/v1/acme/app/people/$ID/_files/avatar?name=ada.png" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: image/png" \
  --data-binary @ada.png
```

- On a single field the upload **replaces** the file; on a `multiple` one it is **appended**. The replaced object is deleted afterwards by the workers.
- The answer is `201` with the document, `Location` the new file's URL.
- The name comes from `?name=`, then the `Content-Disposition` filename, then `file`.
- Every upload is a change to the document: it bumps `_meta.version` and accepts `If-Match` (`412` when the document moved).
- Before any byte is read, backd checks the document is readable, `If-Match`, the `update` rule and `max_files` (`409 too_many_files`). The size is checked while streaming (`413`), and the type from the first bytes (`415 unsupported_file_type`). SVG and other active formats are only accepted when a field lists them.
- A field with `upload: direct` answers `409 upload_mode_mismatch`.
- Bodies stream: they never sit in memory, and large files go to the bucket in parts. `BACKD_MAX_UPLOAD_BYTES` (default 100 MiB) caps what a proxy upload may be; a field's effective limit is the smaller of it and `max_size`. It replaces `MAX_BODY_BYTES` for upload requests only.

## Downloading

- **`download: presigned`** (the default): `302` to a link of the storage that works for a few minutes (`presigned_ttl`, field, then realm, then 5 minutes; at most 7 days) and forces a download. The response is `Cache-Control: private, no-store`. Ask for `?link=json` to get `{"url": …, "expires_at": …}` instead of a redirect.
- **`download: proxy`**: backd streams the file with `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox` and `Content-Disposition: attachment`. It supports one `Range` (`206`, `416`), a strong `ETag` from the file's sha256 with `If-None-Match` (`304`) and `If-Range`. Shared caches may keep a file only when its field has `cache` **and** the document is readable by anyone; otherwise it is `private, no-store`.
- `404 file_missing` when the object has vanished from the storage, `503 storage_unavailable` when the storage can't be reached.

## Links for apps

An `<img>` or a download button can't send an `Authorization` header. Two ways to get a link:

- `GET …/_files/{field}/{file id}?link=json`.
- **`?file_links=true` on any document read** (get or list, data or admin): every file in the answer gets `url` and `expires_at`. They are never stored and not part of the `ETag`.

In `presigned` mode the link is the storage's. In `proxy` mode it is a **backd link**, signed with a per-realm key (the realm secret `BACKD_FILES_LINK_KEY`, made on first use): bound to that file, expiring, valid for as many requests as it takes (`Range`, seeking, retries), and `403 invalid_file_link` when changed or expired. Setting a new value for the secret invalidates every link in circulation.

## Removing

`DELETE` of one file or of the whole field is a change like an upload: the `update` rule sees the document as it would be left, the version bumps, `If-Match` applies, and the objects are queued for deletion once the document no longer references them.

## When things go wrong

Every upload is written to a journal before its bytes go to the bucket, and a worker finishes what a crash interrupted: an object stored but never attached to a document is deleted after a grace period, and a deletion that failed is retried with backoff. Run workers (`backd worker`, or `backd serve --with-worker`) wherever files are used.

| Status | `code` | When |
|---|---|---|
| 403 | `invalid_file_link` | A backd link that was changed, has expired or was signed with a rotated key |
| 404 | `file_missing` | The document holds the file but the storage doesn't |
| 409 | `too_many_files` | A `multiple` field is at `max_files` |
| 409 | `upload_mode_mismatch` | The field takes direct uploads |
| 413 | `payload_too_large` | The file is over `max_size` or `BACKD_MAX_UPLOAD_BYTES` |
| 415 | `unsupported_file_type` | The detected type isn't one of the field's `types` |
| 503 | `storage_unavailable` | The realm's storage can't be reached or its keys aren't set |
