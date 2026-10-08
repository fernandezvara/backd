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
- A field with `upload: direct` answers `409 upload_mode_mismatch`: its files go through [direct uploads](#direct-uploads).
- Bodies stream: they never sit in memory, and large files go to the bucket in parts. `BACKD_MAX_UPLOAD_BYTES` (default 100 MiB) caps what a proxy upload may be; a field's effective limit is the smaller of it and `max_size`. It replaces `MAX_BODY_BYTES` for upload requests only.

## Creating a document with its files

A new document has no id to upload to yet, and a **required** file field can't wait for one. So upload first, then name the upload in the write:

```sh
# 1. The file, before the document exists: answers an upload_id and a secret upload_token
curl -X POST "$API/v1/acme/app/claims/_files/receipt/uploads?name=receipt.pdf" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/pdf" --data-binary @receipt.pdf

# 2. The document, naming the upload where the file goes
curl -X POST "$API/v1/acme/app/claims" -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"title": "Taxi", "receipt": {"upload": "fl_…", "token": "fut_…"}}'
```

The second request is validated and answered like any create, with the file's details in the field. The same reference works in a `PUT` and a `PATCH`: a single field's file is replaced, and a `multiple` field's gets the new files after the ones it holds (a list of references adds several; `409 too_many_files` past `max_files`).

- **The token is the proof.** Only its hash is kept, and it is shown once. An upload made by a signed-in user can only be used by that user; one made by an anonymous caller by whoever holds the token. A leaked `upload_id` alone does nothing.
- **Used once, and for its field.** An upload belongs to one collection and field, is attached by one write, and expires after the realm's `pending_ttl` (1 hour by default). A reference that is wrong, used, expired or someone else's is a `400 validation_error` that doesn't say which.
- **A write that fails gives the upload back:** a refused rule or a validation error leaves it usable for the next try.
- **Who may start one:** a signed-in user, when the collection has a `create` rule; an anonymous caller, when the `create` rule admits a document holding just that file. Each caller holds at most **20 unused uploads** and may start a limited number per ten minutes (`429`, with `Retry-After`). Unused uploads and their objects are deleted by the workers after they expire.
- **Rules** see the final document, with the file in it, for every caller alike: see [Rules for files](../rules/#creating-with-files). Batches don't take references yet.
- The upload endpoint takes the same body and refuses what an upload to a document does: `413`, `415 unsupported_file_type`, `503 storage_unavailable`.

The [JavaScript client](../../clients/js/#files) does all of this (and chooses between proxy and direct on its own): `uploadFile`, `prepareUpload`, `fileUrl`, `downloadFile`. `GET …/_files/{field}` answers how a field takes files (`upload`, `download`, `multiple`, `max_files`, `max_size`, `types`).

## Direct uploads

With `upload: direct` the bytes never pass through backd: the client sends them to the bucket with a link backd signed, then backd checks what arrived. It suits large files (up to the field's `max_size`, at most 5 GiB, one signed `PUT`) and keeps their traffic off backd.

```sh
# 1. Declare the file: all four are required. The update rule is asked here, before anything is signed.
curl -X POST "$API/v1/acme/app/people/$ID/_files/video/uploads" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"name": "talk.mp4", "size": 52428800, "type": "video/mp4", "sha256": "9f86…"}'
# → {"upload_id": "fl_…", "upload_token": "fut_…", "method": "PUT", "url": "https://…", "headers": {…}, …}

# 2. Send the file to the bucket, with exactly the headers named
curl -X PUT "$URL" -H "Content-Type: video/mp4" -H "x-amz-checksum-sha256: …" --data-binary @talk.mp4

# 3. Complete it: backd verifies the file and attaches it to the document
curl -X POST "$API/v1/acme/app/people/$ID/_files/video/uploads/$UPLOAD_ID/complete" \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"upload_token": "fut_…"}'
```

- **The storage enforces what you declared.** The link has the length, the type and the SHA-256 signed in, so the bucket refuses a body that differs. The `size` can't be over the field's `max_size`, nor the `type` outside its `types`.
- **backd verifies, never trusts.** Completing is one `HEAD` (the size and the checksum the storage computed must be the declared ones) and one read of the first bytes, whatever the file's size, to detect its type. Any mismatch deletes the object: `422 upload_mismatch`, or `415 unsupported_file_type` when the content isn't one of the field's types. The `sha256` in a file's details is therefore always one the storage verified.
- **Complete it with the token,** the same way a [pending upload](#creating-a-document-with-its-files) is attached; nothing uploaded yet answers `409 file_not_uploaded` and it can be tried again. The upload can be used once and lives as long as the realm's `pending_ttl`; the link itself lasts the field's `presigned_ttl`.
- **Before the document exists,** start with `POST …/_files/{field}/uploads` (no document id) and the same declaration: completing answers `200` and the upload is named in a create, `PUT` or `PATCH` like any pending upload. They count among the caller's 20 unused uploads.
- **The bucket needs CORS** for the app's origin to accept the browser's `PUT`: see your provider's page. `backd storage check` reads the rules where the provider lets it and warns when they couldn't carry one.
- Presigned multipart uploads (files above 5 GiB) are not available.

## Downloading

- **`download: presigned`** (the default): `302` to a link of the storage that works for a few minutes (`presigned_ttl`, field, then realm, then 5 minutes; at most 7 days) and forces a download. The response is `Cache-Control: private, no-store`. Ask for `?link=json` to get `{"url": …, "expires_at": …}` instead of a redirect.
- **`download: proxy`**: backd streams the file with `X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox` and `Content-Disposition: attachment`. It supports one `Range` (`206`, `416`), a strong `ETag` from the file's sha256 with `If-None-Match` (`304`) and `If-Range`. Shared caches may keep a file only when its field has `cache` **and** the document is readable by anyone; otherwise it is `private, no-store`.
- **`?version=<name>`** serves a made [version](../file-fields/#image-versions) of an image instead of the original, with the same rules, links, headers, ranges and caching. In proxy mode its `ETag` is the version's id (it changes when the version is made again), and it is saved as `<name>-<version>.jpg` or `.png`. A version that isn't ready answers `404 version_unavailable`, with its `status` (`pending`, `empty`, `skipped`, `failed`, or `undeclared`) and `reason` in `details`, so an app can tell "processing" from "no preview".
- `404 file_missing` when the object has vanished from the storage, `503 storage_unavailable` when the storage can't be reached.

## Links for apps

An `<img>` or a download button can't send an `Authorization` header. Two ways to get a link:

- `GET …/_files/{field}/{file id}?link=json` (add `&version=<name>` for a version).
- **`?file_links=true` on any document read** (get or list, data or admin): every file in the answer gets `url` and `expires_at`, and so does each of its `ready` [versions](../file-fields/#image-versions) (in its entry of `versions`). They are never stored and not part of the `ETag`. If the realm's storage can't be used (its keys aren't set, or `BACKD_URL` is missing for a function's backd link) the documents are answered **without** links and the reason is logged, so a read never fails because of the storage; a download or `?link=json` answers `503 storage_unavailable`.

In `presigned` mode the link is the storage's. In `proxy` mode it is a **backd link**, signed with a per-realm key (the realm secret `BACKD_FILES_LINK_KEY`, made on first use): bound to that file (or that version: changing `?version=` on it is `403`), expiring, valid for as many requests as it takes (`Range`, seeking, retries), and `403 invalid_file_link` when changed or expired. Setting a new value for the secret invalidates every link in circulation.

## Removing

`DELETE` of one file or of the whole field is a change like an upload: the `update` rule sees the document as it would be left, the version bumps, `If-Match` applies, and the objects are queued for deletion once the document no longer references them.

## When things go wrong

Every upload is written to a journal before its bytes go to the bucket, and a worker finishes what a crash interrupted: an object stored but never attached to a document is deleted after a grace period, and a deletion that failed is retried with backoff. Run workers (`backd worker`, or `backd serve --with-worker`) wherever files are used.

| Status | `code` | When |
|---|---|---|
| 403 | `invalid_file_link` | A backd link that was changed, has expired or was signed with a rotated key |
| 404 | `file_missing` | The document holds the file but the storage doesn't |
| 404 | `version_unavailable` | `?version=` names a version that isn't `ready` (or isn't declared); `details` carry its `status` and `reason` |
| 409 | `too_many_files` | A `multiple` field is at `max_files` |
| 409 | `upload_mode_mismatch` | A proxy upload to a direct field, or a direct start on a proxy field |
| 409 | `file_not_uploaded` | A direct upload was completed before its file reached the bucket |
| 422 | `upload_mismatch` | A direct upload's size or SHA-256 isn't the declared one |
| 413 | `payload_too_large` | The file is over `max_size` or `BACKD_MAX_UPLOAD_BYTES` |
| 413 | `quota_exceeded` | The file would take the realm or its owner past the [storage quota](../storage/#quotas) |
| 415 | `unsupported_file_type` | The detected type isn't one of the field's `types` |
| 503 | `storage_unavailable` | The realm's storage can't be reached or its keys aren't set |
