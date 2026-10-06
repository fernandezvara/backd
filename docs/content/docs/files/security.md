---
title: "Security of files"
description: "How backd protects files, what it trusts, what links and tokens allow, and what you are responsible for: bucket, prefix, links, caches and the content you accept."
icon: "shield"
weight: 581
toc: true
---

The files feature was reviewed as a whole before this page: rules on file operations, storage credentials and the endpoint restriction, both upload modes, type detection, links, proxied downloads, pending uploads and their tokens, the journal and deletion queue, reconcile, erasure and the functions API. This page says what came out of it.

## How files are protected

- **The bucket is private, and only backd reaches it.** Files are served through backd, or through a link backd signed for one file. backd connects only to the realm's declared `endpoint` (checked after DNS resolution, with no redirects and no proxy); `public_endpoint` is only written into links and is never connected to.
- **A file is as private as its document.** An upload, a removal and a direct-upload start are `update` operations; a download, a link and `?file_links=true` are `read` operations; a document the caller can't read answers `404` for all of them, never revealing its files. The rule runs **before any byte is stored**, with `data` as the document will be, and again when the file is attached. See [Rules for files](../rules/).
- **Clients never write what describes a file.** The id, name, size, detected type, SHA-256 and time are backd's: a client's write drops them, and a PUT keeps the stored files. The object key is `<prefix>/<realm>/<database>/<collection>/<file id>`: no name, and no part of a request, ever reaches it.
- **The type is what the bytes say.** It is detected from the content, never the client's claim, and `types` is checked against it. **A family such as `image/*` or `text/*` does not admit active content** (SVG, HTML, XML, scripts): a field takes one only when it names it. A field with no `types` takes anything, and serves it only as a download.
- **Downloads can't run in your origin.** A presigned link comes from the storage's origin, never backd's, and forces `attachment`. A proxied download carries `Content-Disposition: attachment`, `X-Content-Type-Options: nosniff` and `Content-Security-Policy: sandbox`. Names are cleaned (no path, no control or direction characters, 255 bytes) and quoted, so a name can't add a header or leave its quotes.
- **Direct uploads are verified, not trusted.** The link has the length, the type and the SHA-256 signed in, so the storage refuses another body; completing then checks the size and the checksum the *storage* computed (a missing one fails) and detects the type from the first bytes. A mismatch deletes the object.
- **Tokens are secrets.** An upload token is 256 random bits, only its SHA-256 is stored, it is compared in constant time, used once, bound to a collection and field and, when a signed-in user made it, to that user, and never logged. An upload id alone attaches or completes nothing, and every wrong, used, expired or foreign reference answers the same `400`.
- **backd's own links** are an HMAC over the realm, database, collection, document, field, file and expiry, with a per-realm key, verified in constant time: a link can't be moved to another file or kept past its time. Rotating the key (setting or deleting the secret `BACKD_FILES_LINK_KEY`) invalidates every one.
- **Backd's keys are not a function's to read.** A function can't declare the realm's storage secrets or the link key as its own secrets: the configuration is refused at startup.
- **Cleanup never touches what a document holds.** A worker checks that no document references an upload before it deletes it, and reconcile only considers objects named like files, older than 24 hours, with no open upload record, and that no declared field references.
- **Limits** bound what a caller can store: `max_size` and `max_files` per field, `BACKD_MAX_UPLOAD_BYTES` for proxy uploads, 20 unused uploads per caller (IPv6 callers counted per `/64`) and a rate for starting them.

## What you are responsible for

- **The bucket and its key.** Keep it private, give the key only what your provider's page lists, on the realm's prefix; encryption at rest is the bucket's setting. **Give each backd instance its own `prefix`:** two instances on one prefix collide, and a [reconcile](../maintenance/#reconciling) on one deletes the other's files.
- **Links outlive rules.** A storage link and a backd link both work for anyone who has them until they expire, even if the document's rules change meanwhile. Keep `presigned_ttl` short on sensitive fields; the default is five minutes.
- **Caches outlive rules too.** With `download: proxy` and `cache`, a shared cache may keep a file of a document anyone can read, and keeps it after you tighten the rule. Use `cache` only for files meant to be public.
- **Set `BACKD_URL` in production.** backd-signed links name backd's public address; without it they are built from the request's `Host` header, which a caller chooses.
- **Anonymous uploads cost storage.** Where a collection's `create` rule admits anonymous callers, each address may hold 20 unused uploads of up to `max_size` for an hour. Keep `max_size` small on such fields and let the edge rate-limit by address. Nothing scans files for malware.
- **A removed file field leaves trusted-looking data.** If you take a field out of `files:` and later declare it again, what the documents still hold in it is read as file details again. Don't re-use a field name for something else; clear it first.
- **Functions with `ctx.admin.db` skip the rules**, files included: review them as privileged code.
- **Backups.** MongoDB backups don't include files: back the bucket up at the provider and run reconcile after a restore.
