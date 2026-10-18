---
title: "Web Push: browsers"
description: "Set up push for browsers: the VAPID key pair from backd push web-keys, the subject, the subscription hosts, and rotating the key."
icon: "globe"
weight: 587
toc: true
aliases: ["/docs/operate/providers/web-push/"]
---

`web.provider: webpush` sends to browsers with the standard Web Push protocol: no account with a vendor, only a **VAPID key pair** that identifies your realm to the browsers' push services (Google, Mozilla, Apple, Microsoft).

## 1. The key pair

```sh
backd push web-keys --realm acme
```

It generates a P-256 key pair, stores the **private** half as the realm secret `VAPID_PRIVATE_KEY`, and prints the **public** half with the `realm.yaml` snippet:

```
push:
  web:
    provider: webpush
    public_key: BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U
    private_key: secret:VAPID_PRIVATE_KEY
    subject: secret:VAPID_SUBJECT
```

## 2. The subject

Browsers' push services use a contact URI to reach you when something goes wrong with your pushes:

```sh
backd secret set --realm acme --name VAPID_SUBJECT      # then type mailto:ops@acme.example or https://acme.example
```

## 3. The app

The app asks the realm for the public key, subscribes with it and registers the subscription: see [Devices](../../../../build/push/devices/) (`GET /_auth/devices/web-key`, then `POST /_auth/devices` with `platform: web` and the subscription as JSON). The JavaScript client does all three with `client.push.subscribeWeb()`.

## 4. The network

Each subscription points at its push service's endpoint. A registration is refused unless the host is in `push.web.endpoint_hosts` (defaults: `fcm.googleapis.com`, `updates.push.services.mozilla.com`, `web.push.apple.com`, `*.notify.windows.com`), and the check is made **again** at delivery, so an endpoint can't make the worker call an internal service. The unit's egress token names only the endpoint hosts of its own devices.

## What backd sends and does

The payload is encrypted for the subscription (RFC 8291, `aes128gcm`) and posted with a VAPID `Authorization` header, plus `TTL`, `Urgency` and `Topic` (from `group`). The service worker receives `{title, body, url, data, tag}`.

| The push service answers | Result |
|---|---|
| `2xx` | `sent` |
| `404`, `410` | `invalid`: the subscription is gone and the device is removed |
| `413` | `failed` (payload too large) |
| `429`, `5xx` | `retry` |
| anything else | `failed` |

A **silent** push is never sent to browsers: they require a visible notification.

## Rotating the key

```sh
backd push web-keys --realm acme --rotate
```

A new key pair makes **every existing subscription useless**: each browser made its own under the old public key. So rotating **removes every web device of the realm** (one audited step, instead of thousands of `invalid` answers trickling in), and apps subscribe again at their next launch: `registerOnLaunch` in the client is the way back. Rotate only when the private key may have leaked or the subject must change, and expect all web users to resubscribe. Without `--rotate` the command refuses to replace an existing key.
