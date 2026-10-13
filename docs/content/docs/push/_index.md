---
title: "Push notifications"
description: "Devices, templates and sending: backd registers the devices of your users and sends their pushes itself."
icon: "bell"
weight: 580
toc: true
---

**backd sends pushes itself.** There are three lanes (FCM for Android, APNs for iOS and Web Push for browsers) and each is a small built-in sender in the worker: you configure credentials once, and your functions only say *who* gets *what*. backd keeps the device registry, the templates and their languages, the per-user limits, the retries per device and the records of what went out.

{{< hint style="note" >}}
Push is being built in steps. What is documented here is what exists: **the device registry**. Templates, sending, delivery and the provider guides arrive with the next steps and are marked as planned.
{{< /hint >}}

| Step | What it gives you | State |
|---|---|---|
| [Devices](devices/) | Apps register the device of the signed-in user; backd keeps them accurate | available |
| Messages and templates | `push:` templates per language, `ctx.push.send` | planned |
| Delivery | Built-in FCM, APNs and Web Push senders, batches and retries | planned |
| Records and test send | What was sent and why not, an audited test send | planned |
| Local development | A capture provider and a Push tab in the Mailbox | planned |

## Turning it on

A realm enables push with a `push:` section in `realm.yaml`. Each platform you support gets a block naming its provider; a platform with no block is refused (`400 platform_not_enabled`).

```yaml
push:
  android:
    provider: fcm
  ios:
    provider: apns          # or fcm, for apps that use the FCM SDK
  web:
    provider: webpush
    public_key: BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U
    endpoint_hosts:         # where a browser's subscription may point; defaults shown
      - fcm.googleapis.com
      - updates.push.services.mozilla.com
      - web.push.apple.com
      - "*.notify.windows.com"
  devices:
    max_per_user: 10        # the least recently seen goes above this
    stale_after: 60d        # a device not seen for this long is removed
```

| Key | Default | What it does |
|---|---|---|
| `push.<platform>.provider` | required | `android`: `fcm`; `ios`: `apns` or `fcm`; `web`: `webpush` or `fcm`; `capture` (development) for any |
| `push.web.public_key` | none | The VAPID public key browsers subscribe with, a P-256 point in base64url |
| `push.web.endpoint_hosts` | the four above | The hosts a web subscription may point at: an exact name, or `*.example.com` for any subdomain |
| `push.devices.max_per_user` | `10` | 1 to 100 |
| `push.devices.stale_after` | `60d` | At least `24h` |

- **`BACKD_SECRETS_KEY` is required**: device tokens are stored encrypted with it, and the senders read the providers' credentials from the realm's secrets. A realm with `push:` and no key refuses to start.
- **Without `push:`** the realm behaves as before: the device endpoints answer `404 push_not_configured`.
- The realm's languages (`default_locale` and `locales`) are shared with [email](../functions/email/#languages); a push is written in the user's own language.
