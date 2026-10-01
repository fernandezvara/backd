---
title: "Webhooks"
description: "Receive callbacks from other services and answer them yourself."
icon: "handshake"
weight: 558
toc: true
---

`mode: webhook` receives a callback from another service — Stripe, GitHub, and the like — and answers it directly: `ctx` carries the raw request instead of parsed input, and the function sets the response's status, headers and body itself instead of returning a JSON value backd interprets.

{{< hint warning >}}
A webhook function is callable by **anyone on the internet** (`invoke` must allow anonymous callers), so the function itself is the only check. Verify the sender's signature against the raw body before doing anything, and deduplicate by the sender's event id: providers retry.
{{< /hint >}}

Payment provider example: `payment_webhook` verifies the sender's HMAC signature, deduplicates the provider's retries by event id, and marks the order paid.

{{< example-file path="workshop/main/_functions/payment_webhook/function.yaml" >}}

{{< example-file path="workshop/main/_functions/payment_webhook/index.ts" >}}

{{< example-file path="workshop/main/_functions/lib/signature.ts" >}}

The provider signs the raw body with the shared secret and sends the result in a header. You can play the provider with `openssl` to try it:

```sh
backd secret set --realm workshop --database main --name PAYMENT_WEBHOOK_SECRET   # the shared secret
BODY='{"id": "evt_1", "type": "payment.succeeded", "order_id": "d3c9ljp8hc2g00b6s1m0"}'
SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac 'whsec_test' | sed 's/^.* //')"
curl -X POST https://api.example.com/v1/workshop/main/_func/payment_webhook -H "x-signature: $SIG" -d "$BODY"
# → 200 ok           (the same request again: 200 already processed; a wrong signature: 400 invalid signature)
```

Real providers each have their own signature scheme (Stripe signs `<timestamp>.<body>` and sends `Stripe-Signature: t=…,v1=…`, for example): follow theirs, but keep the same three steps: verify against the raw body, deduplicate by the provider's event id (a unique index on it makes a repeated insert fail), then act.

- **`ctx.request.headers` is everything the sender sent**, including any `Authorization` or `Cookie` header, all chosen by the sender. Don't log the headers, don't forward them, and don't trust `X-Forwarded-For` or similar. With no credentials, the call is anonymous (`ctx.user` is `null`); see [who is calling](../writing/#who-is-calling-sessions-and-identity).
- **`ctx.request`** replaces `ctx.input`: `ctx.request.body` is the raw request body as a string (never parsed as JSON, so a signature check sees exactly the bytes the sender signed — parsing and re-serializing first would break it if the byte-for-byte JSON ever differs, e.g. key order or whitespace); `ctx.request.headers` is a plain object of lower-case header names to string values (several values for the same header are folded into one, comma-joined, as HTTP itself treats them). Request bodies aren't required to be JSON at all — the caller's `Content-Type` isn't checked.
- **The function's return value is the response**, not a JSON body backd wraps: `{ status, body, headers? }`. `status` becomes the HTTP status (defaults to `200` if left out or invalid); `body` is written as-is (defaults to empty); `headers` are set on the response (`Content-Type` defaults to `text/plain; charset=utf-8` unless the function sets its own). `input.schema.json`, `output.schema.json` and `ctx.error` don't apply — there's no caller-facing error envelope to shape, only the response the function itself constructs.
- **`invoke` must allow anonymous callers** (checked at startup, not just documented — see [above](../reference/#invoke-rules)): a webhook sender has no `backd` credential, so the function itself is the only thing that can tell a genuine request from a forged one. That's exactly what step 1 above does; skipping it means anyone who finds the URL can trigger the function.
- **A full concurrency limit answers `503`, not `429`** (unlike `sync`) — Stripe, GitHub, and most webhook senders retry automatically on a 5xx, so this makes a temporary limit resolve itself; a `4xx` typically means "don't bother resending."
- Secrets, network access, `ctx.db`/`ctx.admin.db`, logs, and invocation history all work exactly as for `sync`.
