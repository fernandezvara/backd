---
title: "DigitalOcean Spaces"
description: "Use a DigitalOcean Space as a realm's storage."
icon: "water"
weight: 575
toc: true
---

```yaml
storage:
  provider: digitalocean
  region: nyc3                       # the Space's region; the endpoint is derived
  bucket: acme-files
  prefix: prod
  access_key: secret:STORAGE_ACCESS_KEY
  secret_key: secret:STORAGE_SECRET_KEY
```

| | |
|---|---|
| Endpoint | `https://<region>.digitaloceanspaces.com` (derived from `region`; if you write it, `region` must agree) |
| Addressing | virtual-hosted |
| Signed SHA-256 (direct uploads) | supported (documented in the [Spaces API reference](https://docs.digitalocean.com/reference/api/spaces/)) |
| Largest single PUT | 5 GiB |
| Multipart uploads (files above 5 GiB) | not enabled until the provider smoke test confirms a SHA-256 per part: `max_size` stays at 5 GiB |
| `backd storage check` reads | neither CORS nor encryption: it says so |

## Setting it up

1. **Create the Space** in the region you will name, with *File listing* restricted (private).
2. **Create a Spaces access key** (API → Spaces Keys), scoped to that Space with *Read, Write and Delete*. The key and its secret are the two realm secrets.
3. **Encryption at rest:** Spaces encrypts data at rest; there is nothing to set.
4. **CORS**, needed for [direct uploads](../transfers/#direct-uploads) and cross-origin downloads: set it in the Space's Settings for your app's origin with `GET`, `PUT` and `HEAD`, and allow the headers `Content-Type` and `x-amz-checksum-sha256`. `backd storage check` can't read it: try an upload from the app.
5. Set the secrets and run `backd storage check --realm <realm>`.

A Spaces key is scoped to a Space, not to a prefix: use a Space per `backd` instance, or keep `prefix` distinct and accept that a key can reach every prefix of its Space.
