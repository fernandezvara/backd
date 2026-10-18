---
title: "Local development"
description: "The capture provider, steering outcomes with test tokens, the Mailbox Push tab and fakePush for unit tests."
icon: "terminal"
weight: 584
toc: true
aliases: ["/docs/operate/providers/local/"]
---

You don't need a device or a provider key to develop with push.

## The capture provider

Give a platform `provider: capture`: the worker then **stores each delivery** in a `push_outbox` collection of the realm's system database instead of calling a provider, with the message as it would have gone out and the outcome the device was given.

```yaml
push:
  android: { provider: capture }
  web:
    provider: capture
```

It needs no credentials and no secrets, but it stores message content, so **startup refuses it unless `BACKD_DEV=true`**. Captured deliveries are kept for 24 hours.

### Steering the outcome

The result of a captured delivery depends on the device's token, so pruning, retries and failures can all be seen without a real provider:

| Token starts with | Result |
|---|---|
| anything else | `sent` |
| `test-invalid-` | `invalid`: the device is removed |
| `test-fail-` | `failed`: counted, the device stays |
| `test-retry-` | `retry` on the first attempt, `sent` on the next |
| `test-retry-always-` | `retry` every time, until the unit runs out of attempts and the device counts as `failed` |

An app (or a `curl`) registers such a device the usual way: `POST /_auth/devices {"platform": "android", "token": "test-invalid-1"}`.

## The Mailbox Push tab

The [Mailbox](../../../../build/functions/email/#developing-without-a-provider) of the local stack has a **Push** tab next to *Email*: it lists the captured deliveries, newest first, by platform, outcome and time, and shows each one's title, body, link, data, device and send. Open <https://localhost:8443/example/mailbox/?realm=workshop&tab=push> (or press *Push*).

It reads `GET /v1/<realm>/_dev/push`, which has **no authentication**, like the email outbox it sits next to, and exists only for a realm with a capture provider.

## In unit tests

`@backd/functions-testing` has `fakePush()`, and `createContext({ push: true })` gives a function `ctx.push.send`, recorded and checked like backd would before queueing. See [Testing functions](../../../../build/functions/testing/).

```ts
const { ctx, sentPushes } = createContext({ push: { kinds: ["order-shipped"] } });
await handler(ctx);
assertEquals(sentPushes()[0].kind, "order-shipped");
```
