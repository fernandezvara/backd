---
title: "Files in apps"
description: "Showing, uploading and downloading files from a web app: links for pictures, download buttons that fetch a fresh link on click, and the pitfalls."
icon: "web"
weight: 580
toc: true
---

An app reaches files through the [JavaScript client](../../clients/js/#files), which makes the requests described in [Uploads and downloads](../transfers/). This page is about what to do with them in a page.

## Uploading

`uploadFile(id, field, file, { onProgress })` puts a file in a document, and `prepareUpload(field, file)` makes one for a document that doesn't exist yet: name its `ref` in the create. Both work whether the field takes uploads through backd or straight to the bucket; the client asks the field (`fileField(field)`), which also tells the page `max_size`, `types` and `max_files` to check a file before sending it.

For a field with `upload: direct`, the browser sends the file to the bucket, so the bucket's **CORS** must allow the app's origin ([your provider's page](../) says how, and `backd storage check` looks). A refused upload shows as `upload_failed`; that is nearly always the CORS.

## Showing files

Render a list from the document's file details (`name`, `size`, `type`), which are in every read and need no link. For a picture or a preview shown at once, ask for links with the read:

```js
const doc = await claims.get(id, { fileLinks: true })
img.src = doc.photo.url
```

Every file then carries `url` and `expires_at`. Links last the field's `presigned_ttl` (five minutes by default), and a page can stay open much longer, so `fileLinks` is for what is shown right away: a gallery, a thumbnail grid. Links aren't stored and aren't part of the `ETag`.

## Download buttons

A releases page may list dozens of files; fetching a link for each when the page loads makes the list slow and the links stale before anyone clicks. Make the link **when it is clicked**:

```js
button.onclick = () => releases.downloadFile(release.id, 'assets', file.id)
```

`downloadFile()` gets a fresh link and opens it with `location.assign()`. The link forces a download (`Content-Disposition: attachment`), so the browser saves the file and the page stays where it is. Without a browser, `fileUrl()` returns the link.

- **Render from the metadata,** with no links; fetch a link on click.
- **`fileLinks: true` only for what is shown at once.** Links expire while the page is open.
- **A plain `<a href>` to backd's download route has no credentials:** the browser doesn't send the session token, so it answers `401` or `404`. Use a link from `fileUrl()`, which is its own credential.
- **`window.open(url)` after an `await` is blocked** as a popup, because the click's permission is gone by then. `downloadFile()` navigates the current page instead, which isn't blocked.
- **An expired link answers `403`:** from the storage, or `403 invalid_file_link` from backd. Get a new one; don't cache links.
- **When a click needs logic of its own,** counting downloads, checking a licence, choosing the latest asset of a release, call a [function](../../functions/files/#a-download-that-is-counted) that returns the link, and open the `url` it answers.

## Who can use a link

A link works for anyone who has it until it expires, even if the document's rules change afterwards. Keep `presigned_ttl` short on fields that hold anything sensitive. The files of a document the user can't read are never revealed: asking for their link is a `404`.
