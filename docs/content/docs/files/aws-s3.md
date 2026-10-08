---
title: "AWS S3"
description: "Set up an S3 bucket and a least-privilege key for a backd realm."
icon: "cloud"
weight: 572
toc: true
---

```yaml
storage:
  provider: aws
  region: eu-west-1                  # the bucket's region; the endpoint is derived
  bucket: acme-files
  prefix: prod
  access_key: secret:STORAGE_ACCESS_KEY
  secret_key: secret:STORAGE_SECRET_KEY
```

| | |
|---|---|
| Endpoint | `https://s3.<region>.amazonaws.com` (derived from `region`; if you write it, `region` must agree) |
| Addressing | virtual-hosted (`<bucket>.s3.<region>.amazonaws.com`) |
| Signed SHA-256 (direct uploads) | supported |
| Largest single PUT | 5 GiB |
| Multipart uploads (files above 5 GiB) | supported, with a SHA-256 per part |
| `backd storage check` reads | CORS and default encryption |

## Setting it up

1. **Create the bucket** in the region you will name, with *Block all public access* on. The bucket is always private.
2. **Encryption at rest:** S3 encrypts new objects by default (SSE-S3); choose SSE-KMS in the bucket's default encryption if you need your own key. `backd` sends no encryption headers and `backd storage check` reports what it reads.
3. **Create an IAM user (or role) for `backd`** with this policy, and no other:

   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       { "Effect": "Allow",
         "Action": ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"],
         "Resource": "arn:aws:s3:::acme-files/prod/*" },
       { "Effect": "Allow",
         "Action": ["s3:ListBucket", "s3:GetBucketLocation", "s3:GetBucketCORS", "s3:GetEncryptionConfiguration"],
         "Resource": "arn:aws:s3:::acme-files" }
     ]
   }
   ```

   `prod` is the `prefix`; a staging instance of `backd` gets its own prefix and its own policy. Listing is only for a manual reconcile and for `backd storage check`.
4. **CORS**, needed for [direct uploads](../transfers/#direct-uploads) and for pages that fetch links across origins. Allow your app's origin with `GET`, `PUT` and `HEAD`, the headers `Content-Type` and `x-amz-checksum-sha256`, and expose `ETag`:

   ```json
   [{"AllowedOrigins": ["https://app.example.com"], "AllowedMethods": ["GET", "PUT", "HEAD"],
     "AllowedHeaders": ["Content-Type", "x-amz-checksum-sha256"], "ExposeHeaders": ["ETag"], "MaxAgeSeconds": 3600}]
   ```

   `backd storage check` reads the rules and warns when a browser couldn't PUT with them.
5. **Set the two secrets** and run `backd storage check --realm <realm>`.

## Backups

MongoDB backups don't include files. Turn on bucket versioning and a lifecycle rule at AWS for what you want to keep, and run a reconcile after restoring a database backup.
