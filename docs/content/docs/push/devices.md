---
title: "Devices"
description: "How apps register the devices that receive a user's pushes, and the rules that keep the registry accurate."
icon: "smartphone"
weight: 581
toc: true
---

A **device** is one app installation (or one browser) that can receive pushes for a user. Apps register it with `POST /_auth/devices`; backd owns the registry, so it can move tokens between users, drop them when a session ends and clean up after erasure, which an app's own collection can't.

```sh
curl -X POST "$BACKD/v1/acme/_auth/devices" \
  -H "Authorization: Bearer $SESSION" -H 'Content-Type: application/json' \
  -d '{"platform": "android", "token": "<FCM registration token>", "name": "Ana'"'"'s Pixel"}'
```

```json
{"id": "d3c9ljp8hc2g00b6s1m0", "platform": "android", "name": "Ana's Pixel", "created_at": "…", "last_seen": "…"}
```

## The endpoints

All of these need a **user session**: an API key (or `X-Backd-On-Behalf-Of`) can't register devices.

| Endpoint | What it does |
|---|---|
| `POST /_auth/devices` with `{platform, token, name?}` | Registers or refreshes the device and returns it, **never its token**. Call it at **each launch** and after signing in |
| `GET /_auth/devices` | The caller's devices, most recently seen first |
| `DELETE /_auth/devices/{id}` | Removes one of the caller's devices; someone else's is `404` |
| `GET /_auth/devices/web-key` | `{public_key}` for `pushManager.subscribe`. Public and cacheable; `404` when the realm has no web platform |

### Tokens by platform

| Platform | `token` |
|---|---|
| `android` | The FCM registration token |
| `ios` | The APNs device token, or the FCM token when `ios` uses `provider: fcm` |
| `web` | The browser's push subscription as JSON: `{"endpoint": "https://…", "keys": {"p256dh": "…", "auth": "…"}}` |

A token is at most 4 KiB. A web subscription must be an `https` endpoint on the standard port, on a host the realm allows (`push.web.endpoint_hosts`), and carry both keys; otherwise `400 invalid_device`. The host check matters: without it anyone could register an internal address and have the worker call it.

## The rules

- **One owner per token.** A token exists once per realm. Registering one that belongs to someone else **moves** it to the caller, with a new id: a shared or second-hand phone never receives the previous user's pushes.
- **A web subscription is its endpoint.** A browser that subscribes again gets new keys under the same endpoint; that is the same device, and its keys are replaced.
- **Refresh.** Registering a known token for the same user updates `last_seen`, `name` and the session.
- **The cap.** At most `devices.max_per_user` per user; registering one more removes the least recently seen.
- **They go with their session.** Signing out, or revoking the session that registered a device, removes it. Revoking all of a user's sessions (a password reset or change, an administrator) removes all of the user's devices. A session that merely expires is covered too: a worker removes devices whose session is gone.
- **Stale devices.** A device not seen for `devices.stale_after` is removed.
- **Erasure.** Erasing a user deletes their devices.
- **Rate limit.** 30 registrations per session per hour (`429`).

{{< hint style="tip" title="Already done for you" >}}
Call `POST /_auth/devices` at every launch: it is what keeps a device from going stale, and it is how a token moves when the phone changes hands.
{{< /hint >}}

## From apps

The [JavaScript client](../client/) wraps these endpoints (`client.push`) and keeps a device registered at every launch and sign-in.

## For operators

```sh
backd push devices --realm acme --email ada@example.com
backd push remove-device --realm acme --email ada@example.com --id d3c9ljp8hc2g00b6s1m0
```

The admin API has `GET /_admin/users/{id}/devices` and `DELETE /_admin/users/{id}/devices/{device_id}` (the `users` area). Neither shows a token; a removal is audited as `device.removed`.
