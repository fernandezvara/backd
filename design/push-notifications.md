# Design: push notifications

- **Issues:** features 12.0–12.9 (milestone `12-push-notifications`)
- **Status:** decided; delivery moved into backd (built-in Go senders)
- **Related designs:** [email-delivery.md](./email-delivery.md) (the same template system, languages, limits and Mailbox — but email delivery stays a function), [internal-functions.md](./internal-functions.md), [account-lifecycle.md](./account-lifecycle.md) (sign-out, revocation and erasure remove devices), f6 (job steps)

## 1. The idea

**backd sends pushes itself. All you write is `ctx.push.send`.** Unlike email, where providers number in the dozens, push has exactly three lanes: FCM for Android, APNs for iOS and Web Push for browsers. Each is a small built-in Go sender in the `worker` process — the developer configures credentials once and never writes delivery code.

backd does everything else:

- the device registry;
- templates and languages;
- splitting large audiences;
- per-user limits;
- retries per device;
- pruning dead tokens;
- send records.

Principles carried over from earlier designs:

- **One controlled way out:** provider calls go through the same egress proxy as function calls, with credentials held as realm secrets.
- **No new way of thinking:** push uses the same config shape, template system, Mailbox and least-privilege declarations as email; delivery itself is a backd feature, not user code.
- **Everything belongs to the realm:** devices, send records and counters live in `<realm>___system`, so a realm moves between database servers with everything it needs.
- **No installed base:** breaking changes to earlier designs are allowed (see §6.1).
- **Same conventions as the rest of backd:** credentials are `secret:NAME` references to realm secrets (as `providers:` and `storage:` do), set with `backd secret set`; a missing secret is a startup warning and a `503` at use, never a refusal to start.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Device tokens | backd owns a registry per realm (`<realm>___system.devices`), managed through `/_auth/devices` |
| Device rules | token unique per realm (a web subscription is identified by its endpoint) and moved on re-registration; refreshed at each launch; at most 10 per user; removed after 60 days unseen, when its session ends (sign-out, revocation or expiry), on erasure, and when the provider reports it invalid |
| Who sends | only functions declaring `push: true` (`ctx.push.send`), plus an audited admin test send; no client endpoint |
| Content | named templates with locales, in the same system as email, or raw `title` and `body` from the function; the language is the user's `user.locale` (the realm's `default_locale` when they have not chosen one) |
| Fields | portable core (`title`, `body`, `link`, `data`, `badge`, `sound`, `group`, `ttl`, `priority`, `silent`) plus a `platform` passthrough; the final payload of each enabled platform is checked against its provider's limit before queueing; `silent` isn't sent to web |
| Delivery | built-in Go senders in the worker (`fcm`, `apns`, `webpush`, plus `capture` for development); each delivery unit returns a result per device |
| Packing | at most 100 users per delivery unit; sends above 1,000 users are split into independent batch jobs of 1,000; unit ids are deterministic, so a re-run batch never duplicates a unit |
| Retries | only devices marked `retry` are re-sent; a wholesale sender failure retries the whole unit (duplicates possible, documented) |
| Limits | per user, 5 per minute and 24 per day by default; no exemption of any kind; no per-call cap |
| Web | standard Web Push (VAPID) as the `web` platform; tokens are opaque, so FCM web tokens also work |
| Providers | built-in senders for FCM, APNs and Web Push, scoped per platform in `realm.yaml`; `backd push web-keys`; one in-depth setup guide per provider |
| Records | one record per send with counts, kept 30 days, no message content |
| Development | the `capture` provider writes deliveries to `push_outbox`, test-token prefixes steer results, a Push tab in Mailbox, `fakePush()` for unit tests |
| Needs | `BACKD_SECRETS_KEY` (device tokens are encrypted and credentials are secrets): `push:` without it refuses to start |
| Languages | the list is top-level in `realm.yaml` (issue 12.0), shared with email and valid without either |
| Not configured | a realm without `push:` behaves as today: device endpoints answer `404` and `push: true` functions fail startup |

## 3. Flow

```mermaid
sequenceDiagram
    participant App as App (device)
    participant A as backd API
    participant Fn as App function (push: true)
    participant Q as Job queue (<realm>___system.jobs)
    participant W as backd worker
    participant P as Provider (FCM, APNs, browser push service)
    App->>A: POST /_auth/devices (token, platform) at each launch
    Fn->>A: ctx.push.send(users or audience, kind or title/body, data)
    A->>A: validate message, render every locale, check 4 KiB
    A->>Q: send record + batch jobs of up to 1,000 users
    A-->>Fn: send id, queued, limited, no devices
    W->>Q: claim batch job
    W->>W: per-user limits, resolve devices, render per user
    W->>Q: delivery units of up to 100 users (push_delivery jobs)
    W->>Q: claim delivery job, load device tokens
    W->>P: provider requests through egress
    P-->>W: accepted, unregistered, throttled, ...
    W->>W: result per device (sent, invalid, retry, failed); prune invalid, re-queue retry, update totals
```

## 4. Configuration (`realm.yaml`)

The `realm.yaml` written by `backd template realm` contains this section **commented**, explaining every key, the template layout and the provider setup guides.

```yaml
push:
  android:                             # a platform is enabled by having a block
    provider: fcm                      # built-in senders: fcm, apns, webpush, capture
    credentials: secret:FCM_SERVICE_ACCOUNT   # realm secret holding the service-account JSON
  ios:
    provider: apns                     # 'fcm' is also allowed, for apps on the FCM SDK
    key: secret:APNS_KEY               # realm secret holding the .p8 signing key
    key_id: "AB12CD34EF"
    team_id: "T123456789"
    topic: "com.acme.app"              # the app's bundle id
    sandbox: false                     # api.sandbox.push.apple.com in development
  web:
    provider: webpush
    public_key: "BEl62iU..."           # VAPID public key, printed by backd push web-keys
    subject: secret:VAPID_SUBJECT      # realm secret, e.g. "mailto:ops@acme.example"
    private_key: secret:VAPID_PRIVATE_KEY   # written by backd push web-keys
    endpoint_hosts:                    # hosts a web subscription may point at; defaults shown
      - fcm.googleapis.com
      - updates.push.services.mozilla.com
      - web.push.apple.com
      - "*.notify.windows.com"
  delivery:                            # tuning for the built-in senders; defaults shown
    timeout: 60s                       # per delivery unit
    retry: { attempts: 5, backoff: 30s }
    parallelism: 20                    # provider requests in flight per worker
  limits:
    per_user: { per_minute: 5, per_day: 24 }
  devices:
    max_per_user: 10
    stale_after: 60d
  send_retention: 30d
```

Credentials are realm secrets written `secret:NAME`, the same convention as `providers:` and `storage:`; the private VAPID key is stored as a realm secret by `backd push web-keys` (§12). The realm's languages are not under `push:`: they are the top-level `default_locale` and `locales` (§6.1).

**Startup checks:**
- every platform block names a known provider and has its full credential set, each written `secret:NAME`;
- `BACKD_SECRETS_KEY` is set (device tokens are encrypted with it and the senders read secrets);
- a named secret that isn't set is a **warning** at startup and a `503 push_unavailable` when a send or a registration needs it, like a provider or the storage keys: config is written before secrets are set;
- `provider: capture` fails startup outside development (same enforcement as `dev_only`);
- a `web` block requires a valid P-256 `public_key` and a non-empty `endpoint_hosts` list (defaults applied when absent);
- every template exists in every listed locale and parses (§6);
- every function declaring `push:` belongs to a realm with `push:` configured;
- the old per-email language keys are not present (§6.1).

**Without `push:`:**
- `/_auth/devices` answers `404 push_not_configured`;
- functions declaring `push: true` fail startup;
- the realm behaves as today.

## 5. Device registry (issue 12.1)

### 5.1 Stored per device

Devices are stored in `<realm>___system.devices`, one document per device:

| Field | Notes |
|---|---|
| `id` | server-generated |
| `user_id` | owner |
| `session_id` | the session that registered it |
| `platform` | `android`, `ios` or `web` |
| `token_hash` | SHA-256 of the device's identity, unique per realm, used for lookups and moves: the token itself for `android` and `ios`, the subscription's **endpoint** for `web` (a browser that subscribes again gets new keys under the same endpoint: that is the same device) |
| `token` | encrypted with the realm key (AES-256-GCM), like secrets |
| `name` | optional label shown to the user, e.g. "Ana's Pixel" |
| `created_at`, `last_seen` | |

For `web`, the token is the browser's Push API subscription serialized as JSON (`endpoint` plus `keys.p256dh` and `keys.auth`); registering a known endpoint replaces the stored keys. For `android` it is the FCM registration token, and for `ios` it is the APNs device token — or the FCM token when `ios` uses `provider: fcm`. Web tokens from the FCM web SDK aren't subscriptions and only work with `web` set to `provider: fcm`, which the FCM web SDK requires anyway.

### 5.2 Endpoints

All of these require a user session; API keys and `X-Backd-On-Behalf-Of` can't register devices.

| Endpoint | Behavior |
|---|---|
| `POST /v1/{realm}/_auth/devices` with `{ platform, token, name? }` | Registers or refreshes a device and returns `id`, `platform`, `name`, `created_at` and `last_seen`, but never the token. Apps call it at **each launch** and after sign-in. |
| `GET /v1/{realm}/_auth/devices` | Lists the caller's devices without tokens. |
| `DELETE /v1/{realm}/_auth/devices/{id}` | Removes one of the caller's devices; someone else's device answers `404`. |
| `GET /v1/{realm}/_auth/devices/web-key` | Returns `{ public_key }`. Public and cacheable. Answers `404` when `web` isn't enabled. |

### 5.3 Rules

- **One owner per token:** a token is unique per realm. Registering a token that already exists moves the device to the caller's user and session, so a shared or second-hand phone never receives the previous user's pushes.
- **Refresh:** registering an existing token for the same user and session updates `last_seen`, `name` and (web) the subscription keys.
- **Cap:** at most `devices.max_per_user`. Registering one more removes the least recently seen device.
- **Stale devices:** a worker removes devices unseen for `devices.stale_after`.
- **Removed with their session:** signing out or revoking the registering session removes its devices. Revoking all of a user's sessions (password reset or change, admin action) removes all of the user's devices.
- **Session expiry too:** a session that expires (idle time or maximum lifetime) ends without anyone calling anything, so a device is delivered to only while its registering session exists and hasn't expired: batches resolve devices through the session, and the stale-device sweep removes the devices of sessions that are gone.
- **Erasure (4.6):** erasing a user deletes their devices, and pending batches then find no devices for that user.
- **Invalid tokens:** a device reported `invalid` by its provider is deleted.
- **Validation:**
  - `platform` must have a configured provider block in `push:` (`400 platform_not_enabled`);
  - tokens are at most 4 KiB;
  - a `web` token must be a valid subscription whose `endpoint` is `https` and whose host matches `push.web.endpoint_hosts` (`400 invalid_device`).
- **Rate limit:** 30 registrations per session per hour, counted with the existing counters.

**Why the endpoint host check matters:** a web subscription's endpoint is a URL chosen by the client. Without the check, anyone could register `https://internal-service/...` and make the worker's webpush sender call it. The host list stops this at registration, and the egress proxy checks it again at delivery.

### 5.4 Operators

- `GET /_admin/users/{id}/devices` and `DELETE /_admin/users/{id}/devices/{device}`, both audited.
- `backd push devices --realm <r> --email <address>` lists a user's devices without tokens.

## 6. Messages and templates (issue 12.2)

### 6.1 Languages *(issue 12.0)*

Email and push share one list of languages, at the top level of `realm.yaml` and independent of both features:

```yaml
default_locale: en
locales: [en, es]
```

`user.locale` is mapped against this list as at sign-up today (exact, then language, then `default_locale`); it is always a listed value. Every listed locale must have every email **and** push template that is in use. `email.default_locale` and `email.locales` are removed (a breaking change, allowed): startup fails and says they moved. [email-delivery.md](./email-delivery.md) §4 and §7 describe the same move. Issue 12.0 does it first, so the milestone starts from the final layout.

### 6.2 Templates

Templates follow the same layout as email, with one folder per kind and one file per locale:

```
<realm>/push/
  order-shipped/   en.yaml  es.yaml
  event-reminder/  en.yaml  es.yaml
```

```yaml
# <realm>/push/order-shipped/en.yaml
title: "Your order {{.Data.order_no}} is on its way"
body: "It should arrive {{.Data.eta}}."
link: "acme://orders/{{.Data.order_id}}"     # optional; opened when the notification is tapped
```

- **Engine:** Go `text/template`. Templates are parsed and checked at startup, held in memory and included in the config fingerprint.
- **Data:** `{{.Realm}}`, `{{.User.Locale}}` and `{{.Data}}`. Templates get no email address or other profile fields, because lock screens are visible to others.
- **Declaring kinds:** functions declare the kinds they send, either `push: true` or `push: { kinds: [order-shipped] }` (mirrors email). `kinds` limits named kinds only; raw `title`/`body` sends stay available under either form. Startup checks that every declared kind has a template in every locale. Sending an undeclared kind, or one whose template doesn't exist, answers `unknown_push_kind`.
- **Language per recipient:** `user.locale`, which is always a listed value (the realm's `default_locale` for a user who never chose one). Devices carry no language of their own. One message per user, so all of a user's devices get the same language.
- **Raw messages:** `title` and `body` passed by the function, already localized, with kind `raw`. Used when the text is data, such as a chat preview.
- **No system pushes:** backd sends no pushes of its own. Every push comes from the app.

### 6.3 Fields

| Field | Type | Notes |
|---|---|---|
| `title`, `body` | string | from the template or raw; both absent when `silent` |
| `link` | string | from the template or raw; optional |
| `data` | object of strings | at most 2 KiB serialized; delivered to the app |
| `badge` | integer ≥ 0 | optional |
| `sound` | string | optional; `default` or a bundled sound name |
| `group` | string | replaces an earlier push with the same key (FCM `collapse_key`, APNs `apns-collapse-id`, Web Push `topic`) |
| `ttl` | duration | how long providers keep trying; default `24h`; `0` means now or never |
| `priority` | `normal` or `high` | default `normal` |
| `silent` | boolean | data only, no visible notification; requires `data`, refuses `title`, `body`, `kind`, `badge` and `sound`; delivered to `android` and `ios` only (browsers require a visible notification), a user whose only devices are web counts as `no_devices` |
| `platform` | object | `android`, `ios` and `web` keys merged into that platform's provider payload by the sender |

**Support by platform** (what a field does where it isn't supported is nothing, never an error):

| Field | `android` (FCM) | `ios` (APNs / FCM) | `web` |
|---|---|---|---|
| `title`, `body` | yes | yes | yes |
| `link` | `data` key the app opens | `data` key the app opens | `url` for the service worker |
| `data` | yes | yes | yes |
| `badge` | ignored | yes | ignored |
| `sound` | yes | yes | ignored |
| `group` | `collapse_key` | `apns-collapse-id` (at most 64 bytes) | `Topic` (at most 32 characters) |
| `ttl`, `priority` | yes | yes | yes |
| `silent` | yes | yes | not sent |

**Size:** at send time backd renders the message in every listed locale with the call's `data`, shapes it with each **enabled** platform's sender (the payload the provider will receive, envelope included) and rejects it with `payload_too_large` before anything is queued when a shaped payload is over that provider's limit: 4 KiB for FCM and APNs, 3,993 bytes of plaintext for Web Push (RFC 8291). The shaping is a pure function of the message, so nothing is called to check it.

## 7. Sending (issue 12.3)

### 7.1 `ctx.push.send`

```js
const r = await ctx.push.send({
  users: [order.owner],              // or user: "u_1"; or audience (§7.2)
  kind: "order-shipped",             // or title + body (raw)
  data: { order_no: order.no, order_id: order.id, eta: "tomorrow" },
  group: `order-${order.id}`,
  priority: "high",
  wait: false,                       // true: wait for the totals within the function's time limit
});
// r = { send_id: "ps_...", users: 1, jobs: 1, queued: 1, limited: 0, no_devices: 0 }
```

- **Who can use it:** only functions whose `function.yaml` declares `push: true` (or `push: { kinds: [...] }`) have `ctx.push`. Startup logs which functions can send.
- **No per-call cap:** `users` may be any length.
- **Return value:** for a send that fits in one batch (up to 1,000 users), the batch is planned immediately, so the result already includes `limited` and `no_devices`. For larger sends those counts are `null` and appear in the send record as batches run.
- **`wait: true`:** waits for the final totals (`sent`, `invalid`, `failed`, `limited`, `no_devices`) within the function's own time limit, then returns them. When the limit is reached, it returns what it has so far.
- **Errors:** the function receives `FunctionError` with `payload_too_large`, `unknown_push_kind`, `invalid_push_message` or `push_not_configured`.

### 7.2 Audiences given as a filter

A list of a million user ids can't go through the callback channel. So `send` also accepts `audience`:

```js
await ctx.push.send({ audience: { roles: ["member"], email_verified: true }, kind: "new-version" });
await ctx.push.send({ audience: "all", kind: "maintenance" });
```

- **What can be filtered:** a fixed set of user fields, validated strictly (anything else is `invalid_push_message`): `roles` (users holding any of them), `email_verified` (boolean), `locale` (a listed locale) and `created_before` / `created_after` (timestamps). `"all"` is every active user. Disabled and erased users are never matched. The admin user list has no such filter today (it searches by address), so the audience query is its own repository method with these fields, written once and tested.
- **Paging:** backd pages through the matching users by id when creating batches, so the audience is fixed when each batch is created. A send with an audience starts as a planner job that creates the batches page by page and records its position, so an audience of a million users never sits in memory and a restarted planner continues where it was.
- **Access:** requires `push: { audience: true }` in `function.yaml`, so reaching everyone is visible at startup.

### 7.3 Batches and delivery calls

- **Batch jobs:** a send is split into **batch jobs of up to 1,000 users**. Each batch is an independent job in `<realm>___system.jobs` with its own lease, progress and failure, so one failing batch doesn't affect the others.
- **What a batch does:** a worker claims a batch, applies the per-user limits (§8), resolves each user's devices, renders each user's message and splits the users into **delivery units of up to 100 users**. Each unit is a `push_delivery` job handled by the built-in senders. The batch reports progress as job steps (f6) and completes once all of its units are finished.
- **A batch can run again.** A worker may die mid-batch, and its lease then expires. So a batch records its progress as job steps and creates each delivery unit with a **deterministic id** (`<batch id>-<index>`); creating a unit that exists is a no-op, and the limit counters are charged when a unit is created (a unit that exists was charged already). The re-run batch continues after its last recorded unit and no user receives a push twice because of it.
- **Retries:** each delivery unit follows `push.delivery.retry` (5 attempts, 30 s backoff by default):
  - devices marked `retry` by their provider are re-queued as the **next attempt of the same unit, with only those devices**, after the backoff;
  - a wholesale sender failure (network down, credentials rejected) retries the whole unit. Devices already delivered in that attempt may get the push twice; the guide says so;
  - after the last attempt, devices still pending are counted as `failed`.

## 8. Limits

- **Per user:** `push.limits.per_user`, 5 per minute and 24 per day by default. A user is counted once per push, however many devices they have.
- **What counts:** silent pushes count too. There is **no exemption of any kind**: no urgent flag and no admin bypass. The test send counts as well.
- **Counters:** the existing counters collection in `<realm>___system` (TTL expiry), keyed `push:user:<id>:m` and `push:user:<id>:d`. They are incremented when a batch accepts a user.
- **Over the limit:** the user is skipped and counted as `limited`. Skipped users are logged by id only.
- **No per-call cap.** Very large sends are kept in check by batching, the per-user limits and the sender's bounded parallelism (`push.delivery.parallelism`, 20 by default).

## 9. Delivery

A `push_delivery` job carries a **delivery unit**: the send id, the kind, and up to 100 recipients. A recipient is a user id, the message rendered in their locale, and their device ids — the unit stores device ids only, and the worker loads and decrypts the tokens when it claims the job, so tokens never sit in the job queue.

The worker groups the unit's devices by platform and hands each device to the sender for its configured provider, with `parallelism` concurrent provider requests in flight. Each sender answers one result per device:

| Status | Meaning | Effect |
|---|---|---|
| `sent` | provider accepted | counted; `provider_id` kept in worker logs only |
| `invalid` | token is dead | the device is deleted (§5.3) |
| `retry` | throttled or transient | re-queued as the unit's next attempt |
| `failed` | permanent for this push | counted; the device is kept |

**Per-provider mapping** (the senders' whole job is this table plus payload shaping):

- **FCM** (HTTP v1): `UNREGISTERED` or 404 → `invalid`; `INVALID_ARGUMENT` on the token → `invalid`; 429 or 5xx → `retry`; else `failed`. The OAuth2 access token is minted from the service-account secret and cached for the unit.
- **APNs** (HTTP/2, token auth): 410 `Unregistered` or 400 `BadDeviceToken` → `invalid`; 429 or 5xx → `retry`; else `failed`. The ES256 JWT is reused within the unit.
- **Web Push** (VAPID): 404 or 410 → `invalid`; 429 or 5xx → `retry`; 413 → `failed`; else `failed`. Payload encrypted per RFC 8291 in Go.
- **capture** (development): writes the delivery to `push_outbox` instead of calling out (§11).

**Operational details:**

- **Timeouts:** `push.delivery.timeout` (60 s default) bounds a whole unit; a unit holds several hundred devices, so senders use bounded parallelism rather than one request per device.
- **Wholesale failures:** a network error or rejected credentials — failures that aren't per-device — error the job, and the unit's next attempt re-sends every pending device (duplicates possible, documented in the guide).
- **Masking:** tokens and `data` values are masked in worker logs, like secrets.
- **Egress:** provider requests go through the egress proxy. The worker holds the egress signing key (the executor never does) and mints a token per delivery unit naming only that unit's hosts: the providers' own (`fcm.googleapis.com`, `oauth2.googleapis.com`, `api[.sandbox].push.apple.com`) plus, for webpush, the realm's `endpoint_hosts`. Without an egress proxy (a single-process development setup) the senders use the same dialer as the sign-in providers' client, which dials only public addresses of the host it was asked for. Either way an endpoint that resolves to a private address is refused.

## 10. Send records, metrics and test send (issue 12.4)

- **Send record:** one per send in `<realm>___system.push_sends`. It holds:
  - `id` and `kind` (or `raw`);
  - the calling function (or `admin:<user>` for test sends) and `created_at`;
  - its batches and their status;
  - totals: users, `queued`, `limited`, `no_devices`, `sent`, `invalid`, `retry` (in flight), `failed`;
  - `completed_at`.

  Records expire after `push.send_retention` (30 days). **Titles, bodies, data and tokens are never stored.** User ids live only in the batch and delivery jobs, which follow the existing job retention.
- **Viewing:** `GET /_admin/push/sends` (filters: kind, function, time) and `GET /_admin/push/sends/{id}`; `backd push sends --realm <r> [--id]`. The admin UI is a follow-up.
- **Test send:** `backd push test --realm <r> --email <address> (--kind <k> [--data json] | --title --body)` and `POST /_admin/push/test`. It sends to one user, needs an admin role, is **audited** and counts toward that user's limits. Its record shows the admin as the sender.
- **Metrics (5.1):** `backd_push_users_total` with `outcome` (`queued`, `limited`, `no_devices`), `backd_push_deliveries_total` with `status`, and `backd_push_devices_pruned_total` with `reason` (`invalid`, `stale`, `cap`, `moved`), all labeled by realm.

## 11. Development and tests (issue 12.5)

- **`provider: capture`:** a platform block can use the `capture` sender instead of a real provider. It stores each delivery (recipient, device, platform, rendered message, attempt) in a `push_outbox` collection in `<realm>___system` — no function, no schema to write, it comes with the worker. It is development-only: startup refuses it outside dev mode, like `dev_only`. Results depend on the token:
  - normal tokens return `sent`;
  - tokens prefixed `test-invalid-` return `invalid`, so pruning can be seen;
  - tokens prefixed `test-retry-` return `retry` on the first attempt and `sent` afterwards;
  - tokens prefixed `test-fail-` return `failed`;
  - tokens prefixed `test-retry-always-` keep returning `retry` until they fail.
- **Mailbox:** gains a **Push** tab next to email, listing captured pushes by user, device, platform, status and time, and showing the rendered message, `data` and the send's limit results. Links are shown, not opened.
- **`@backd/functions-testing`:** `fakePush()` records `ctx.push.send` calls for unit tests of app functions.
- **Sender tests:** the built-in senders are covered by Go unit tests against recorded provider responses (success, unregistered, bad token, throttled, server error, payload too large), checking the status mapping in §9.
- **Integration tests:** they read `push_outbox` and the send record.

## 12. Providers and credential setup (issue 12.6)

The three senders live in backd's worker. Each provider is configured per platform in `realm.yaml` (§4) with credentials held as realm secrets.

### 12.1 FCM (Android — and iOS when `provider: fcm`)

Setup steps, documented in depth in the FCM guide:

1. Create or pick a project in the [Firebase console](https://console.firebase.google.com).
2. **For Android:** add an Android app with the app's package name; download `google-services.json` for the app build.
3. **For iOS over FCM:** add an iOS app with the bundle id, download `GoogleService-Info.plist`, then upload an **APNs authentication key** (see §12.2) under Project settings → Cloud Messaging → Apple app configuration. The APNs key lives in Firebase; backd never sees it.
4. Enable **Firebase Cloud Messaging API (V1)** in the linked Google Cloud project (APIs & Services → Library).
5. Project settings → Service accounts → **Generate new private key** → download the JSON.
6. Store it: `backd secret set --realm <r> --name FCM_SERVICE_ACCOUNT < service-account.json`, then `push.android.credentials: secret:FCM_SERVICE_ACCOUNT` in `realm.yaml`.

The sender mints OAuth2 tokens from the service account (`oauth2.googleapis.com`) and POSTs to `fcm.googleapis.com/v1/projects/<id>/messages:send`; `project_id` comes from the JSON.

### 12.2 APNs (iOS, `provider: apns`)

Setup steps, documented in depth in the APNs guide:

1. In the [Apple Developer account](https://developer.apple.com/account): Certificates, Identifiers & Profiles → Keys → **+** → enable **Apple Push Notifications service (APNs)** → download the `.p8` file (one-time download) and note the 10-char **Key ID**.
2. Note the **Team ID** from the membership page (top right of the account).
3. The **topic** is the app's bundle id (e.g. `com.acme.app`); the app must have the Push Notifications capability enabled under its App ID.
4. Store the key: `backd secret set --realm <r> --name APNS_KEY < AuthKey_XXXX.p8`, then set `key: secret:APNS_KEY`, `key_id`, `team_id`, `topic` and `sandbox` in `realm.yaml`.
5. `sandbox: true` targets `api.sandbox.push.apple.com` — required for development builds; production builds (TestFlight/App Store) need `sandbox: false`. The two environments have separate device tokens, so a realm running both needs care: the guide recommends sandbox only in development realms.

The sender builds an ES256 JWT (signed with the `.p8`, `iss` = team id, `kid` = key id, refreshed at most hourly) and POSTs HTTP/2 to `api[.sandbox].push.apple.com/3/device/<token>`.

### 12.3 Web Push (`provider: webpush`)

Setup steps, documented in depth in the Web Push guide:

1. `backd push web-keys --realm <r>` — generates a VAPID P-256 key pair, stores the private key as the realm secret `VAPID_PRIVATE_KEY`, and prints the public key and the `push.web` snippet for `realm.yaml`. It refuses to overwrite an existing key without `--rotate`.
2. Set the contact URI: `backd secret set --realm <r> --name VAPID_SUBJECT` with a `mailto:` or `https:` value, and `subject: secret:VAPID_SUBJECT` in `realm.yaml` (browser push services use it to reach the operator when something goes wrong).
3. The app calls `pushManager.subscribe` with the public key (§13) and registers the resulting subscription.

The sender encrypts each payload per RFC 8291 and POSTs to the subscription's endpoint with a VAPID-signed JWT (`sub` = subject, `aud` = endpoint origin).

**Rotating (`--rotate`):** a new key pair invalidates every existing web subscription, because each browser created it under the old public key. Rotation therefore deletes every `web` device in the realm (one audited sweep, rather than thousands of `invalid` results trickling through sends), and apps resubscribe at their next launch — `registerOnLaunch` (§13) is the recovery path. The Web Push guide documents this plainly: rotate only when the key is compromised or the subject must change, and expect all web users to resubscribe.

- **Guides:** one per provider, each covering the full credential walkthrough above, the `realm.yaml` keys, which egress hosts to allow, and sandbox versus production for APNs.
- **Egress:** a test confirms that HTTP/2 to APNs works through the egress proxy.

## 13. JavaScript client and example app (issue 12.7)

- **`client.push.register({ platform, token, name? })`:** stores the returned device id for the session. `client.push.devices()` lists the user's devices, and `client.push.unregister(id?)` removes one (the current device by default).
- **`client.push.subscribeWeb({ registration })`:** fetches the web key, calls `pushManager.subscribe({ userVisibleOnly: true, applicationServerKey })` and registers the subscription. It does nothing when permission is denied, and returns the permission state.
- **Re-registration:** `client.push.registerOnLaunch(getToken)` re-registers after sign-in and at start, as the device rules expect.
- **Service worker example:** shows the notification and opens `link` on click. Docs for mobile apps describe the raw HTTP endpoints until platform clients exist.
- **Example app:** approving an expense sends an `expense-approved` push to the person who submitted it, through a `push: true` function. Locally the realm uses `provider: capture`, and the result can be checked in the Mailbox Push tab.

## 14. Security considerations

- **Who can reach users:** only functions that declare `push`, and only through the realm's own templates or their own code. There is no client endpoint, and every test send is audited.
- **Spam:** per-user limits with no exemption, so even a bug in a function can't flood a phone.
- **Tokens:**
  - encrypted at rest, looked up by hash;
  - never returned by the API;
  - masked in worker logs;
  - never in send records or job payloads (delivery units carry device ids only).
- **Wrong recipient:** a token moves to its new owner on re-registration. Sign-out, revocation, erasure, the cap and the stale-device cleanup remove devices.
- **SSRF through web subscriptions:** endpoints are checked against the allowed hosts at registration and again by the egress proxy (or the public-address dialer) when the webpush sender calls them.
- **Pushes after the session is gone:** a device is used only while its registering session lives; expiry is covered like sign-out (§5.3).
- **Duplicates and double charges:** a re-run batch can't create a unit twice (§7.3).
- **Lock screen content:** templates receive no profile fields beyond the locale. The docs advise against personal data in titles and bodies and recommend `silent` plus a fetch in the app for sensitive content.
- **Retention:** send records hold counts only, for 30 days.
- **Credentials in core:** provider secrets (service account, `.p8` key, VAPID private key) are realm secrets read by the worker's senders — the same store and masking as function secrets, with no user code in between.
- **Development only:** startup refuses `provider: capture`, which stores message content, outside development.
- **Browsers and silent pushes:** not sent to web, so a site can't be made to run background work invisibly.

## 15. Documentation

- **"Push notifications" guide:** the built-in delivery model; configuration with every key; templates and languages; fields and size; sending and audiences; batches, retries and possible duplicates; limits; send records; local development with `provider: capture` and the Push tab in Mailbox.
- **Provider guides:** FCM, APNs and Web Push, each an in-depth credential walkthrough (§12) plus the `realm.yaml` keys and egress hosts. The Web Push guide covers `backd push web-keys`, including rotation: it deletes every web device and apps resubscribe at next launch.
- **`function.yaml` reference:** `push` (`true`, `kinds`, `audience`).
- **`realm.yaml` template and reference:** `push:` and the shared `locales`.
- **Security model page:** device rules, limits, SSRF check, data on lock screens, provider credentials held as realm secrets.
- **`api/openapi.yaml`:** `/_auth/devices`, `/_admin/push/*` and the codes `push_not_configured`, `platform_not_enabled`, `invalid_device`, `payload_too_large`, `unknown_push_kind`, `invalid_push_message`.

## 16. Issues

| id | GitHub | Issue | Depends on |
|---|---|---|---|
| 12.0 | #270 | One list of languages at the top level of `realm.yaml` | 4.2e |
| 12.1 | #260 | Device registry and `_auth/devices` | 12.0, 4.6 |
| 12.2 | #261 | `push:` configuration, templates and message validation | 12.1 |
| 12.3 | #262 | `ctx.push.send`, audiences, batches and delivery units | 12.2, 4.2a, 4.2c, f6 |
| 12.4 | #263 | Send records, metrics, admin API and test send | 12.3, 5.1 |
| 12.5 | #264 | Capture provider, Mailbox Push tab and test helpers | 12.3, 4.2d |
| 12.6 | #265 | Built-in provider senders (FCM, APNs, Web Push) and setup guides | 12.5 |
| 12.7 | #266 | Push in the JavaScript client and example app | 12.1, 12.5 |
| 12.9 | #271 | Push in the admin interface | 12.1, 12.4 |
| 12.8 | #267 | Security review of push | 12.6, 12.7, 12.9 |

## 17. Alternatives considered

- **Tokens in app collections, or both:** rejected. Only backd can clean up on sign-out, revocation and erasure, and move tokens between users.
- **One call per device:** too many processes. **One call per send:** one failure affects everyone, and calls grow without bound.
- **Templates only, or raw text only:** chose both, with the email template system.
- **Only `title`, `body`, `link` and `data`, or the FCM message passed through:** chose a portable core plus a passthrough.
- **Provider topics:** bypass the registry and tie the design to FCM.
- **A language per device, as a fallback:** rejected on 2026-10-14. `user.locale` is always a listed value, so a device language could never apply; a person who wants another language changes theirs.
- **Hashing the whole web subscription:** rejected; a browser that subscribes again produces new keys under the same endpoint, and that is one device.
- **An urgent flag skipping limits:** rejected, because any exemption is a way to spam.
- **A per-call cap:** rejected. Batches of 1,000 keep large sends manageable instead.
- **Web push only through FCM's SDK, or no web push:** chose standard Web Push with opaque tokens.
- **Logs only for development, or a separate simulator:** chose the Mailbox Push tab.
- **Delivery functions, as email uses:** rejected on 2026-10-13 in favor of built-in senders (§12). Push has exactly three providers, so the function indirection bought no provider freedom and left every realm writing the same delivery code. Recorded in `decisions.md`.
- **Full delivery history with content:** rejected for privacy and growth.
- **Any function can send, or a client endpoint with rules:** rejected for least privilege.

## 18. Follow-ups

- Clients for other platforms (Dart, Swift, Kotlin) with device helpers, as in `missing-features.md`.
- Pushes from data events once triggers (27.3–27.6, functions after data changes) exist, using `push: true` functions.
- **Other push providers** (HMS, Xiaomi, OneSignal, …): the provider scope is fixed at the built-in three; new providers land as additional built-in senders if demand appears.
- Notification preferences per user and kind (mute categories), if apps ask for it.
