---
title: "Connecting storage"
description: "storage: in realm.yaml, the access keys as realm secrets, the prefix each backd instance needs, and backd storage check."
icon: "cloud_upload"
weight: 571
toc: true
---

A realm connects its storage with a `storage:` section in its [`realm.yaml`](../../configuration/realm/). It needs `auth` enabled, like everything that keeps secrets in the realm's system database.

```yaml
storage:
  provider: minio                    # aws | minio | r2 | digitalocean
  endpoint: https://files.example.com
  region: us-east-1
  bucket: acme-files
  prefix: prod                       # required; unique per backd instance sharing the bucket
  access_key: secret:STORAGE_ACCESS_KEY
  secret_key: secret:STORAGE_SECRET_KEY
  download: presigned                # presigned | proxy
  presigned_ttl: 5m
  pending_ttl: 1h
  quota:
    realm: 10GiB                     # optional
    user: 1GiB                       # optional
```

| Setting | Meaning |
|---|---|
| `provider` | **Required.** One of `aws`, `minio`, `r2`, `digitalocean`. Anything else stops `backd` at startup. |
| `endpoint` | Only the scheme and host, `https://<host>[:port]`; the bucket goes in `bucket`, never in the endpoint. Required for `minio` and `r2`; `aws` and `digitalocean` derive it from `region`. It must have the shape the provider's [page](../) says, or startup stops and names the setting. |
| `public_endpoint` | **`minio` only.** The address signed links are made for when browsers can't reach `endpoint` (`backd` at `http://minio:9000`, browsers at `http://localhost:9000`). `backd` never connects to it: it is only written into links. |
| `region` | `aws` and `digitalocean` read it from the endpoint (and refuse one that disagrees); `r2` is always `auto`; `minio` accepts any (default `us-east-1`). |
| `bucket` | The bucket's name, which must already exist. |
| `prefix` | **Required.** Starts every object key (`<prefix>/<realm>/…`). See below. |
| `access_key`, `secret_key` | **Required**, written `secret:NAME`: the names of two [realm secrets](../../functions/secrets/), never the keys themselves. |
| `download` | The realm's default for how files are delivered: `presigned` (a redirect to a signed link, the default) or `proxy` (streamed through `backd`). |
| `presigned_ttl` | How long a signed link works: `10s` to `7d`, default `5m`. |
| `pending_ttl` | How long an upload nobody attached to a document is kept: `1m` to `7d`, default `1h`. |
| `quota.realm`, `quota.user` | Optional limits on the bytes documents reference: for the whole realm, and for the documents one user owns. Sizes like `500MiB` or `10GiB`. See [Quotas](#quotas). |

There is no `path_style` setting (the provider decides how a bucket is addressed) and no `sse` (encryption at rest is the bucket's default). `backd template realm` writes the section commented, with an example for each provider.

## Quotas

`storage.quota` limits what documents may hold, measured with the [usage totals](../maintenance/#usage): the bytes of the files that documents reference.

- `realm` caps the whole realm. `user` caps the files of the documents **one user owns** (the document's owner). Documents without an owner, and anything made with an API key or by anonymous callers, count for the realm only.
- An upload that would pass a limit is refused with **`413 quota_exceeded`**, before any byte is stored when the size is known (a proxy upload with a `Content-Length`, a direct upload's declared size) and while streaming when it isn't. Replacing the file of a single field counts only the difference.
- A [pending upload](../transfers/#creating-a-document-with-its-files) counts for its creator, and attaching it to a document is checked again, so a document can't be given more than its owner may hold.
- Removing a file or a document frees the quota once the file is queued for deletion, without waiting for the workers.
- Enforcement is **approximate under concurrency**: uploads that run at the same moment each see the totals before the others, so a limit can be passed by what a few simultaneous uploads add. It protects against runaway use, not to the byte.

The admin UI's *Storage* page, `backd storage usage` and `GET /_admin/storage` show the usage against the limits.

## The access keys

The keys are secrets of the realm: set them with `backd secret set`, **without** `--database`, which makes them realm-scope secrets.

```sh
backd secret set --realm acme --name STORAGE_ACCESS_KEY
backd secret set --realm acme --name STORAGE_SECRET_KEY
```

They are encrypted under `BACKD_SECRETS_KEY`, which an instance serving a realm with `storage:` therefore needs (without it the keys can't be read and the realm's file endpoints answer `503 storage_unavailable`), and applied within a minute of a change, so rotating a key needs no restart. Give `backd` a key that can only use the realm's bucket and prefix: put, get, head and delete on `<prefix>/*`, and listing on the bucket for that prefix (only a manual reconcile lists). Each provider's page has the policy.

A realm whose keys aren't set yet starts, with a warning naming the secrets, and its file endpoints answer `503 storage_unavailable` until they are.

## The prefix

`prefix` is required and starts every object key of the instance. **Each `backd` instance that shares a bucket needs its own** (`prod`, `staging`, a developer's): two instances on the same prefix would write over each other's files, and a reconcile on one could delete the other's. The prefix is one name of lower-case letters, digits, dots, hyphens and underscores.

## Checking it

```sh
backd storage check --realm acme
```

The running `backd` (which holds the keys) verifies the storage the way files will use it, and the command prints each step and **exits `1` when one failed**:

| Step | What it proves |
|---|---|
| `bucket` | The bucket exists and the keys reach it (a listing of the realm's prefix, so a key limited to its prefix works; a refused listing is a warning, since only a reconcile lists). |
| `write`, `head`, `read`, `range` | A small object can be stored under `<prefix>/<realm>/_check/`, read back identical and read by range. |
| `list` | The prefix can be listed (only a reconcile needs this: a failure is a warning). |
| `checksum` | The storage **rejects** an upload whose `x-amz-checksum-sha256` doesn't match: direct uploads rely on this. The report says whether it is `verified` or `ignored`. |
| `download link`, `upload link` | Signed links work, force a download, and refuse different content. Skipped when links are for a `public_endpoint` `backd` can't reach: open one from a browser instead. |
| `cors` | The bucket's CORS rules, where the provider exposes them: browsers need them for [direct uploads](../transfers/#direct-uploads) (the `PUT` method and the `Content-Type` and `x-amz-checksum-sha256` headers) and for cross-origin downloads. It warns when a rule set couldn't carry a direct upload. |
| `encryption` | The bucket's default encryption, where the provider exposes it. |

The objects it makes are deleted again; nothing else in the bucket is touched. `--json` prints the report as JSON, and it is the admin API's [`POST /_admin/storage/check`](../../auth/admin/#endpoints) (audited as `storage.check`; the `config` area, read is enough).

## What `backd` connects to

`backd` talks to object storage **directly**, not through the functions' [egress proxy](../../functions/network/) (egress exists for function code; the storage is `backd`'s own). To keep that exception small, the storage client:

- connects **only to the realm's declared endpoint** (and, for virtual-hosted providers, the bucket's name under it), checked when the connection is made, after the name is resolved;
- refuses link-local and cloud-metadata addresses, follows no redirect, and uses no HTTP proxy;
- never connects to `public_endpoint`.

In production, give `backd` a route to the declared storage and nothing else for this: the [production reference](../../operations/production/) puts MinIO on a network only `backd` and its worker share, so no function process has a route to it, and its test proves it.

## Startup checks

`backd` refuses to start, naming the file and the setting, for an unknown `provider`, an `endpoint` or `region` that doesn't fit the provider, a `public_endpoint` on a provider that doesn't allow one, a missing `prefix`, `bucket` or key, a `download`, `presigned_ttl` or `pending_ttl` out of range, `storage:` in a realm with `auth: disabled`, and plain `http` outside `BACKD_DEV=true`. A realm whose keys can't be read (no `BACKD_SECRETS_KEY`, or the secrets not set) starts with a warning instead.

## When something is wrong

| `backd storage check` says | Usually |
|---|---|
| `bucket … refused the credentials` | A wrong access key or secret, or a key that can't use this bucket. Set the secrets again. |
| `the bucket doesn't exist` | A wrong `bucket`, or the key can't see it. Create the bucket first: `backd` never creates one. |
| `write … refused` | The key lacks put on `<prefix>/*`. |
| `checksum … ignored` | The storage accepts content that differs from the signed SHA-256: don't use direct uploads with it. |
| `cors … no CORS rules` | Only matters for direct uploads and for pages that fetch links across origins; set CORS for your app's origin. |
| `storage_unavailable` | The two secrets aren't set (or `BACKD_SECRETS_KEY` is missing on this instance). |
