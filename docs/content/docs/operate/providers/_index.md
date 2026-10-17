---
title: "Providers"
description: "Connect external services to a realm: file storage backends and push notification providers."
icon: "plugs-connected"
weight: 100
toc: true
---

`backd` talks to outside services through providers you configure per realm. The two families are independent: they answer different settings, hold different credentials and share nothing.

| Family | Declared in | For | Pages |
|---|---|---|---|
| [File storage](storage/) | `storage:` in `realm.yaml` | a realm's own S3-compatible bucket, holding the files its documents carry | connecting it, then [MinIO](storage/minio/), [AWS S3](storage/aws-s3/), [Cloudflare R2](storage/cloudflare-r2/) or [DigitalOcean Spaces](storage/digitalocean-spaces/) |
| [Push notifications](push/) | `push:` in `realm.yaml` | sending a notification to a user's devices, one provider per platform | [FCM](push/fcm/) (Android, iOS on the FCM SDK), [APNs](push/apns/) (iOS), [Web Push](push/web-push/) (browsers), [local development](push/local/) |

Email delivery is declared in the realm's `email:` settings — see [Email](../../build/functions/email/) for how functions send it.
