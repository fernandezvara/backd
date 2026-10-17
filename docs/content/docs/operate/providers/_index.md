---
title: "Providers"
description: "Connect external services to a realm: file storage backends and push notification providers."
icon: "plugs-connected"
weight: 100
toc: true
---

`backd` talks to outside services through providers you configure per realm:

| Provider | Pages |
|---|---|
| File storage | [Connecting storage](storage/), then [MinIO](minio/), [AWS S3](aws-s3/), [Cloudflare R2](cloudflare-r2/) or [DigitalOcean Spaces](digitalocean-spaces/) |
| Push notifications | [FCM](fcm/) (Android, iOS on the FCM SDK), [APNs](apns/) (iOS), [Web Push](web-push/) (browsers), [local development](local/) |

Email delivery is declared in the realm's `email:` settings — see [Email](../../build/functions/email/) for how functions send it.
