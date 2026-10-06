---
title: "File fields"
description: "files: in collection.yaml: which fields of a document carry files, their limits, and what backd keeps in them."
icon: "attach_file"
weight: 576
toc: true
---

A collection says which fields of its documents carry files under `files:` in its [`collection.yaml`](../../configuration/config-dir/#collectionyaml). The realm needs a [`storage:`](../storage/) section.

```yaml
files:
  avatar:
    max_size: 2MiB
    types: [image/png, image/jpeg, image/webp]
  receipts:
    multiple: true
    max_files: 5
    max_size: 10MiB
    types: [application/pdf, image/*]
  manual:
    max_size: 20MiB
    download: proxy
    cache: 1h
```

| Key | Default | Meaning |
|---|---|---|
| `max_size` | required | The largest one file may be (`512KiB`, `10MiB`, `1GiB`). Checked while the bytes stream in |
| `types` | any | Allowed content types, `image/png` or a family such as `image/*`. Checked against the type **detected from the file's content**, never the one the client claims |
| `multiple` | `false` | The field is a list of files instead of one |
| `max_files` | `10` | With `multiple`: how many files the field may hold (1 to 1000) |
| `upload` | `proxy` | `proxy` streams the bytes through backd; `direct` has the client send them straight to the bucket with a signed link, which backd verifies afterwards ([Direct uploads](../transfers/#direct-uploads)). `max_size` is at most 5GiB for `direct` |
| `download` | the realm's | `presigned` redirects to a short-lived link of the storage; `proxy` streams the file through backd |
| `presigned_ttl` | the realm's | How long this field's links last (at most 7 days) |
| `cache` | none | With `download: proxy`: how long shared caches may keep the file, only when anyone may read the document |

A field name is lower-case letters, digits and underscores. **A file field is declared only here:** one that is also in `schema.json` is a startup error, because backd adds its schema itself, to the validators in MongoDB, to the [rules](../rules/) and to the queries.

## What a file field holds

backd owns it. Clients can't write it: a `POST`, `PUT`, `PATCH` or batch operation drops file fields from the body, and a `PUT` keeps the files the document has. Files change only through [uploads and removals](../transfers/), or by naming a [pending upload](../transfers/#creating-a-document-with-its-files) in a write. A single field holds one object, a `multiple` one a list of them:

```json
{
  "id": "fl_8f3k2n5x0q7w1a9b4c6d",
  "name": "receipt.pdf",
  "size": 48213,
  "type": "application/pdf",
  "sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
  "uploaded_at": "2026-10-06T10:15:00.000Z"
}
```

`type` is the detected content type, `sha256` the digest of the bytes backd stored. Names are cleaned, never rejected: Unicode is normalized (NFC), path and control characters are dropped, the name is cut at 255 bytes and an empty one becomes `file`.

Objects are stored under `<prefix>/<realm>/<database>/<collection>/<file id>` and never move. The name is not part of the key, so renaming or sharing names never touches storage.

## Changing `files:`

- **Tighter limits** (`max_size`, `types`, `max_files`) apply to the next uploads. Files already stored are not touched or re-checked.
- **Removing a field** from `files:` makes what documents hold in it inert: it is no longer served, and the objects stay in the bucket until removed by hand.
- **Rules are checked at startup** with the injected fields known, like any other field.
