---
title: "MinIO"
description: "Use a self-hosted MinIO as a realm's storage, in development and in production."
icon: "dns"
weight: 573
toc: true
---

```yaml
storage:
  provider: minio
  endpoint: https://minio.internal.example:9000   # http://minio:9000 only with BACKD_DEV=true
  # public_endpoint: http://localhost:9000        # when browsers reach MinIO at another address
  bucket: acme-files
  prefix: prod
  access_key: secret:STORAGE_ACCESS_KEY
  secret_key: secret:STORAGE_SECRET_KEY
```

| | |
|---|---|
| Endpoint | any `https://<host>[:port]` (plain `http` only when `BACKD_DEV=true`, for development) |
| Addressing | path style (`<endpoint>/<bucket>/…`) |
| Region | any; default `us-east-1` |
| Signed SHA-256 (direct uploads) | supported |
| Oldest version tested | `RELEASE.2025-09-07T16-13-09Z` (the repository's tests run against Chainguard's current build as well) |
| `backd storage check` reads | neither CORS nor encryption (MinIO doesn't expose them to an S3 client): it says so |

## Development

**Images:** the MinIO project no longer publishes container images (the old `minio/minio` and `quay.io/minio/minio` repositories stopped being pullable). The repository's stacks use [Chainguard's build](https://images.chainguard.dev/directory/image/minio/overview) (`cgr.dev/chainguard/minio` and `cgr.dev/chainguard/minio-client`); both have no shell, so the server can't run a health check and one-shot jobs call `mc` directly. You can also build MinIO from its source. `backd storage check` tells whether whatever you run behaves.

The repository's local stack (`make example`) runs MinIO on `http://localhost:9000` (console `http://localhost:9001`, user `backd-dev`, password `dev-p4ssw0rd!`) and creates a `backd-files` bucket. `backd` reaches it at `http://minio:9000` inside the stack; a browser reaches it at `http://localhost:9000`, so a realm sets both:

```yaml
storage:
  provider: minio
  endpoint: http://minio:9000
  public_endpoint: http://localhost:9000   # links are signed for the address the browser uses
  bucket: backd-files
  prefix: dev
  access_key: secret:STORAGE_ACCESS_KEY
  secret_key: secret:STORAGE_SECRET_KEY
```

`public_endpoint` exists because signing is local and the signed host must be the one the browser uses; `backd` never connects to it. MinIO answers any origin's CORS by default; restrict it with `MINIO_API_CORS_ALLOW_ORIGIN`.

## Production

Run MinIO with TLS (a certificate your CA signs, in MinIO's `certs` folder), on a network only `backd` and its worker can reach, and give them the CA with `SSL_CERT_FILE` if it is a private one. The [production reference](../../operations/production/) does exactly this and tests it.

Create the bucket, a policy limited to your prefix and a key that uses it:

```sh
mc alias set prod https://minio.internal.example:9000 <root user> <root password>
mc mb prod/acme-files
cat > files-prod.json <<'JSON'
{ "Version": "2012-10-17", "Statement": [
  { "Effect": "Allow", "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"],
    "Resource": ["arn:aws:s3:::acme-files/prod/*"] },
  { "Effect": "Allow", "Action": ["s3:ListBucket"],
    "Resource": ["arn:aws:s3:::acme-files"], "Condition": { "StringLike": { "s3:prefix": ["prod/*"] } } },
  { "Effect": "Allow", "Action": ["s3:GetBucketLocation"], "Resource": ["arn:aws:s3:::acme-files"] } ] }
JSON
mc admin policy create prod files-prod files-prod.json
mc admin user add prod backd-files <a long random secret>
mc admin policy attach prod files-prod --user backd-files
```

**Encryption at rest:** MinIO encrypts objects when a KMS (or `MINIO_KMS_SECRET_KEY` in development) is configured and the bucket has default encryption; set it with `mc encrypt set` and confirm it by hand, since `backd storage check` can't read it.

**CORS** is MinIO's API-wide setting (`MINIO_API_CORS_ALLOW_ORIGIN`), not a bucket setting, and it is what [direct uploads](../transfers/#direct-uploads) from a browser need: set it to your app's origin (MinIO answers the headers a signed `PUT` carries, `Content-Type` and `x-amz-checksum-sha256`). `backd storage check` can't read it.
