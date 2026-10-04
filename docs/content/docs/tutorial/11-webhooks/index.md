---
title: "11. Being called by the internet"
description: "the import webhook — mode: webhook, HMAC signatures over the raw body, dedupe by the sender's event id."
weight: 310
toc: true
---

So far everything called your functions *for you* — a page, a schedule, another function. A webhook reverses it: a partner feed pushes assets into the shelf, signed in to nobody. The function is the whole trust boundary.

## The function

{{< example-file path="shelf/main/_functions/import/function.yaml" >}}

- **`mode: webhook`** — `ctx.input` is gone; `ctx.request` holds the raw `{ body, headers }`, and the function returns `{ status, body }` itself.
- **`invoke: "true"`** — anonymous calls must reach it; the sender has no session and never will.
- **`secrets:`** — `IMPORT_WEBHOOK_SECRET` is shared with the sender out of band, stored per-database, never in git.

{{< example-file path="shelf/main/_functions/import/index.ts" >}}

Three steps, in order:

- **Verify the sender against the exact bytes it signed** — `x-signature: sha256=<hex>` is an HMAC of the raw body (`lib/signature.ts`, the same file the workshop uses).
- **Dedupe by the sender's own event id** — providers retry until they get a 2xx, so "already processed" is a normal answer. The unique index on `imports.event_id` makes the second delivery a cheap 409.
- **Then, and only then, act** — fields are whitelisted out of the untrusted JSON, and the asset lands as a **draft**: `published_at` is the publish function's alone, even for a trusted feed ([Webhooks](../../../functions/webhooks/)).

## Feeding it

```sh
# set the shared secret once
backd secret set --realm shelf --database main --name IMPORT_WEBHOOK_SECRET

# then the fake feed — it signs like a real provider
node clients/js/examples/shelf/push.js http://localhost:8080 <secret> "Title" https://example.com
```

`push.js` computes `sha256=<hmac>` over the body and POSTs — what any provider does, minus the account.

## Why no handler test

`createContext` fakes `ctx.input`, `ctx.db`, `ctx.call`, `ctx.email` — but not `ctx.request`, which webhooks alone get (the workshop's `payment_webhook` ships untested for the same reason). What *is* testable is the crypto: `signature.test.ts` proves `sign`/`verifySignature` agree, and that a tampered byte or a wrong secret is refused — which is the part a bad actor probes.

## You should see

- `node push.js …` answers `200 ok` — a new draft appears in My assets for curators to publish.
- The same `event_id` sent twice answers `200 already processed` — one `imports` doc, one asset.
- A wrong `x-signature` answers `400 invalid signature`; missing `event_id`/`title`, `422`.
- `deno test` covers the signature round-trip.

Next: chapter 12 — running it: API keys, the audit feed, and the hardening checklist.
