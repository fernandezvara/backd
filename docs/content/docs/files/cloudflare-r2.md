---
title: "Cloudflare R2"
description: "Use a Cloudflare R2 bucket as a realm's storage."
icon: "cloud_queue"
weight: 574
toc: true
---

```yaml
storage:
  provider: r2
  endpoint: https://<account id>.r2.cloudflarestorage.com   # no bucket in it
  bucket: acme-files
  prefix: prod
  access_key: secret:STORAGE_ACCESS_KEY
  secret_key: secret:STORAGE_SECRET_KEY
```

| | |
|---|---|
| Endpoint | `https://<account id>.r2.cloudflarestorage.com` (also `.eu.` and `.fedramp.` for those jurisdictions). R2's console shows it with the bucket's name after it: write only the host, and the name in `bucket`. |
| Addressing | virtual-hosted |
| Region | always `auto` |
| Signed SHA-256 (direct uploads) | **not confirmed yet** (see below): until it is, direct uploads are not offered for R2 and proxy uploads work |
| Largest single PUT | 5 GiB |
| `backd storage check` reads | neither CORS nor encryption: it says so |

## Setting it up

1. **Create the bucket** in the Cloudflare dashboard (R2 → Create bucket). Keep it private: don't enable public access or a public domain.
2. **Create an API token** (R2 → Manage API tokens) with *Object Read & Write* limited to that bucket. R2 shows an **Access Key ID** and a **Secret Access Key** once: those are the two secrets.
3. **Encryption at rest:** R2 encrypts all objects at rest; there is nothing to set.
4. **CORS**, only for direct uploads and cross-origin downloads: set it on the bucket (Settings → CORS policy) for your app's origin.
5. Set the secrets and run `backd storage check --realm <realm>`.

R2's tokens limit the bucket, not a prefix: the `prefix` still keeps each `backd` instance's files apart, but a key can reach the whole bucket, so give each instance sharing a bucket a token of its own only if you must trust no instance with another's files; otherwise use a bucket per instance.

## The checksum

Direct uploads depend on the storage verifying a signed `x-amz-checksum-sha256`. For R2 this is confirmed by the provider smoke test (`go test ./internal/storage -run R2`, which needs a bucket and a token of your own and uploads a handful of tiny objects), and `backd storage check` reports it on your bucket: `signed SHA-256: verified` or `ignored`.
