---
title: "Push notifications"
description: "Devices, templates and sending: backd registers the devices of your users and sends their pushes itself."
icon: "bell"
weight: 700
toc: true
---

**backd sends pushes itself.** There are three lanes (FCM for Android, APNs for iOS and Web Push for browsers) and each is a small built-in sender in the worker: you configure credentials once, and your functions only say *who* gets *what*. backd keeps the device registry, the templates and their languages, the per-user limits, the retries per device and the records of what went out.

{{< hint style="note" >}}
Push is being built in steps. What is documented here is what exists: **the device registry**, the configuration, the templates, sending, and the records of what was sent, the providers' senders and local development. The JavaScript client and an example app close the set.
{{< /hint >}}

| Step | What it gives you | State |
|---|---|---|
| [Devices](devices/) | Apps register the device of the signed-in user; backd keeps them accurate | available |
| Messages and templates | Templates per language, the message fields and their checks | available |
| [Sending](sending/) | `ctx.push.send`, audiences, batches, limits, retries per device | available |
| Providers | Built-in senders: [FCM](../../operate/providers/fcm/) (Android, and iOS apps on the FCM SDK), [APNs](../../operate/providers/apns/) (iOS) and [Web Push](../../operate/providers/web-push/) (browsers), each with a setup guide | available |
| [Records and test send](records/) | What was sent and why not, an audited test send, metrics, the admin interface's Push page | available |
| [From apps](client/) | `client.push`: register, keep registered, subscribe a browser; the service worker; mobile apps | available |
| [Local development](../../operate/providers/local/) | A capture provider with steerable outcomes, a Push tab in the Mailbox and `fakePush()` | available |

## Turning it on

A realm enables push with a `push:` section in `realm.yaml`. Each platform you support gets a block naming its provider and that provider's credentials; a platform with no block is refused (`400 platform_not_enabled`). Credentials are **realm secrets**, written `secret:NAME` like the keys of [sign-in providers](../auth/providers/) and [storage](../configuration/realm/#storage): the value never goes in the file.

```yaml
push:
  android:
    provider: fcm
    credentials: secret:FCM_SERVICE_ACCOUNT   # the service-account JSON
  ios:
    provider: apns                            # or fcm, with `credentials`
    key: secret:APNS_KEY                      # the .p8 signing key
    key_id: "AB12CD34EF"
    team_id: "T123456789"
    topic: com.acme.app                       # the app's bundle id
    sandbox: false
  web:
    provider: webpush
    public_key: BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U
    private_key: secret:VAPID_PRIVATE_KEY
    subject: secret:VAPID_SUBJECT             # mailto: or https:
    endpoint_hosts:                           # where a browser's subscription may point; defaults shown
      - fcm.googleapis.com
      - updates.push.services.mozilla.com
      - web.push.apple.com
      - "*.notify.windows.com"
  devices:
    max_per_user: 10
    stale_after: 60d
  delivery:
    timeout: 60s
    retry: { attempts: 5, backoff: 30s }
    parallelism: 20
  limits:
    per_user: { per_minute: 5, per_day: 24 }
  send_retention: 30d
```

| Key | Default | What it does |
|---|---|---|
| `push.<platform>.provider` | required | `android`: `fcm`; `ios`: `apns` or `fcm`; `web`: `webpush` or `fcm`; `capture` (development only) for any |
| `push.android.credentials`, `push.ios.credentials` | required with `fcm` | The secret holding the service-account JSON |
| `push.ios.key`, `key_id`, `team_id`, `topic` | required with `apns` | The `.p8` key (a secret), its id, the team and the app's bundle id |
| `push.ios.sandbox` | `false` | `true` targets APNs' sandbox, for development builds |
| `push.web.public_key`, `private_key`, `subject` | required with `webpush` | The VAPID key pair (the private half a secret) and the contact URI (a secret) |
| `push.web.endpoint_hosts` | the four above | The hosts a web subscription may point at: an exact name, or `*.example.com` for any subdomain |
| `push.devices.max_per_user` | `10` | 1 to 100 |
| `push.devices.stale_after` | `60d` | At least `24h` |
| `push.delivery.timeout` | `60s` | A delivery unit's time limit |
| `push.delivery.retry.attempts`, `backoff` | `5`, `30s` | Attempts a unit gets and the wait between them |
| `push.delivery.parallelism` | `20` | Provider requests in flight per worker |
| `push.limits.per_user.per_minute`, `per_day` | `5`, `24` | Pushes a user may get; there is no exemption of any kind |
| `push.send_retention` | `30d` | How long the record of a send is kept (at least `24h`) |

- **`BACKD_SECRETS_KEY` is required**: device tokens are stored encrypted with it, and the senders read the providers' credentials from the realm's secrets. A realm with `push:` and no key refuses to start.
- **A secret that isn't set** is a warning at startup and `503 push_unavailable` when a push needs it: write the configuration first, set the secrets after.
- **`provider: capture`** stores deliveries instead of sending them; startup refuses it unless `BACKD_DEV=true`.
- **Without `push:`** the realm behaves as before: the device endpoints answer `404 push_not_configured`, and a function that declares `push` fails startup.

## Messages and templates

A push is written in the user's own language (`user.locale`, which is always one of the realm's `locales`; the realm's `default_locale` for a user who never chose one), from a **template**, like an email:

```
<realm>/push/
  order-shipped/   en.yaml  es.yaml
```

```yaml
# <realm>/push/order-shipped/en.yaml
title: "Your order {{.Data.order_no}} is on its way"
body: "It should arrive {{.Data.eta}}."
link: "acme://orders/{{.Data.order_id}}"     # optional: opened when the notification is tapped
```

- **One folder per kind, one file per language.** Every language the realm lists needs a file of every kind, or startup fails naming the missing files. Files are parsed and test-rendered at startup, kept in memory and part of the configuration fingerprint. `backd template realm` writes `push/example/en.yaml`.
- **Data:** `{{.Realm}}`, `{{.User.Locale}}` and `{{.Data}}`, the data of the call. Templates get no address or profile field, because lock screens are seen by others.
- **Declaring kinds:** a function that sends pushes declares `push: true`, or `push: { kinds: [order-shipped], audience: false }` in `function.yaml`. `kinds` limits the named kinds; raw `title` and `body` sends stay available. Startup checks that every declared kind has templates, and that the realm has a `push:` section.
- **Raw messages:** a function may pass `title` and `body` itself (kind `raw`), already in the right language, when the text is data, such as a chat preview.
- **No system pushes:** backd sends no pushes of its own; every push comes from your app.

### The message

| Field | Notes |
|---|---|
| `title`, `body` | From the template or raw; both absent when `silent` |
| `link` | From the template or raw; optional. A URL or an app link (`acme://orders/1`), or a path starting with `/` |
| `data` | Strings only, at most 2 KiB serialized; delivered to the app. The names `from`, `message_type`, `collapse_key`, `link` and those starting with `google` or `gcm` are reserved by FCM |
| `badge` | Integer 0 or more |
| `sound` | `default` or a bundled sound name |
| `group` | 1 to 32 letters, digits, `_` or `-`; replaces an earlier push with the same key |
| `ttl` | How long providers keep trying; default `24h`, at most 28 days, `0` is now or never |
| `priority` | `normal` (default) or `high` |
| `silent` | Data only, no visible notification; requires `data` and refuses `title`, `body`, `kind`, `badge` and `sound` |
| `platform` | `android`, `ios` and `web` objects merged into that platform's provider payload |

What a field does where it isn't supported is nothing, never an error:

| Field | `android` | `ios` | `web` |
|---|---|---|---|
| `link` | a `link` key in `data` for the app to open | a `link` key in the payload | `url`, for the service worker |
| `badge` | ignored | yes | ignored |
| `sound` | yes | yes | ignored |
| `group` | `collapse_key` | `apns-collapse-id` / `thread-id` | `Topic` / `tag` |
| `silent` | yes | yes | **not sent**: browsers require a visible notification |

**Size.** The message is rendered in every listed language and shaped the way each enabled platform's provider receives it; one over its provider's limit (4 KiB for FCM and APNs, 3,993 bytes of plaintext for Web Push) is refused with `payload_too_large` before anything is queued, naming the platform and the language.
