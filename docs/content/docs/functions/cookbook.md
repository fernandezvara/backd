---
title: "Cookbook"
description: "Seven complete, tested recipes: a sync function as the caller, a privileged idempotent refund, a background report, a nightly cleanup, a daily digest, a payment webhook and an email delivery function for Postmark."
icon: "integration_instructions"
weight: 565
toc: true
---

Every recipe here is a function of the `workshop` realm ([`examples/config/workshop`](https://github.com/fernandezvara/backd/tree/main/examples/config/workshop) in the repository): a tiny shop with `orders`, `refunds`, `reports`, `digests` and `events`. The code on this page is read straight from those files, and `TestWorkshopExample` runs every function for real (MongoDB, a real executor, a real worker), so what you read is what runs.

## Run the workshop

{{< live-example path="workshop/" text="the workshop tour: every recipe below as a clickable panel, with an inspector" >}}

The repository's local stack serves the realm, and its `backd` runs a worker (`serve --with-worker`), so jobs and schedules work:

```sh
make example                # docker compose up --build; the API is at https://localhost:8443
docker compose exec backd /backd bootstrap --realm workshop --email ops@example.com   # first administrator
# (the workshop tour already offers an operator demo account, so this is only needed to use the CLI)

API=https://localhost:8443/v1/workshop
TOKEN=$(curl -s $API/_auth/signup -H 'Content-Type: application/json' \
  -d '{"email": "ada@example.com", "password": "dev-p4ssw0rd!"}' | jq -r .token)

curl -s $API/main/orders -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"item": "widget", "quantity": 2, "amount": 1000, "status": "draft"}'
# → {"id": "d3c9ljp8hc2g00b6s1m0", "item": "widget", …}
```

The stack sets a dev-only `BACKD_SECRETS_KEY`, which `payment_webhook` needs because it declares a secret; the secret's *value* is set later, in [the webhook recipe](#a-payment-webhook). `backd` starts without it, and only that function answers `500 secret_missing` until it is set.

The `orders` rules let a customer create and edit their own *drafts* and read their own orders; `status` is never theirs to change. Only functions move an order on.

## A sync function that runs as the caller

{{< live-example path="workshop/#orders" text="try it in the tour (step 1)" >}}

`order_total` prices one of the caller's orders. It reads with `ctx.db`, so the orders' read rule decides what exists for this caller: someone else's order is a `404`, the same as reading it directly. The tax rate lives in one place, on the server.

{{< example-file path="workshop/main/_functions/order_total/function.yaml" >}}

{{< example-file path="workshop/main/_functions/order_total/index.ts" >}}

```sh
curl -s $API/main/_func/order_total -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"order_id": "d3c9ljp8hc2g00b6s1m0"}'
# → {"order_id": "d3c9ljp8hc2g00b6s1m0", "subtotal": 1000, "tax": 200, "total": 1200}
```

See [Writing a function](../writing/), including the input and output schemas and why errors go through [`relay`](../writing/#errors).

## A privileged, atomic, idempotent change

{{< live-example path="workshop/#refund" text="try it in the tour (step 2)" >}}

`refund` is something no customer rule may allow, so it is a function with `admin: true`, restricted to staff, that changes the order and records the refund **in one transaction**. `idempotency: required` makes a retried request safe.

{{< example-file path="workshop/main/_functions/refund/function.yaml" >}}

{{< example-file path="workshop/main/_functions/refund/index.ts" >}}

```sh
curl -s $API/main/_func/refund -H "Authorization: Bearer $STAFF_TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: refund-d3c9ljp8hc2g00b6s1m0' -d '{"order_id": "d3c9ljp8hc2g00b6s1m0", "reason": "damaged"}'
# → {"refund_id": "d3c9lq38hc2g00b6s1o0", "order_id": "d3c9ljp8hc2g00b6s1m0", "amount": 1000, "receipt_job": "d3c9lq38hc2g00b6s1p0"}
# the same request again → the same answer, and nothing is refunded twice
# without an Idempotency-Key → 400 idempotency_key_required;  as a customer → 403;  an unpaid order → 409 not_refundable
```

Give someone the `staff` role with `backd user add-role --realm workshop --email … --role staff`. Read more: [idempotency](../calling/#idempotency), [batch writes](../writing/#privileged-changes-admin-and-batch).

## A background report

{{< live-example path="workshop/#export" text="try it in the tour (step 4)" >}}

`export_orders` builds a CSV of the caller's orders. As an `async` function it answers `202` with a job; a worker runs it; the report lands in `reports`, owned by the customer. Running the same job twice writes one report.

{{< example-file path="workshop/main/_functions/export_orders/function.yaml" >}}

{{< example-file path="workshop/main/_functions/export_orders/index.ts" >}}

{{< example-file path="workshop/main/_functions/lib/csv.ts" >}}

```sh
JOB=$(curl -s $API/main/_func/export_orders -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{}' | jq -r .id)
until [ "$(curl -s $API/main/_jobs/$JOB -H "Authorization: Bearer $TOKEN" | jq -r .status)" = done ]; do sleep 1; done
curl -s $API/main/_jobs/$JOB -H "Authorization: Bearer $TOKEN" | jq .result.output
# → {"report_id": "d3c9lpr8hc2g00b6s1n0", "rows": 1, "reused": false}
curl -s $API/main/reports/d3c9lpr8hc2g00b6s1n0 -H "Authorization: Bearer $TOKEN" | jq -r .csv
```

More on [knowing when a job finished](../jobs/#knowing-when-a-job-finished) and [designing jobs that are safe to repeat](../jobs/#designing-jobs-that-are-safe-to-repeat).

## A nightly cleanup

{{< live-example path="workshop/#operating" text="run it by hand in the tour (step 5)" >}}

`nightly_cleanup` runs at 03:00 UTC and deletes draft orders nobody finished in 30 days, in batches of a hundred, using `ctx.admin.db` (a scheduled run has no caller). It is [internal](../internal/): nobody can call it over HTTP. It only deletes what is already past the cutoff, so running twice is harmless.

{{< example-file path="workshop/main/_functions/nightly_cleanup/function.yaml" >}}

{{< example-file path="workshop/main/_functions/nightly_cleanup/index.ts" >}}

```sh
backd functions jobs --function workshop/main/nightly_cleanup --scheduled --since 7d
```

More in [Scheduled functions](../cron/).

## A daily digest on a schedule

{{< live-example path="workshop/#operating" text="run it by hand in the tour (step 5)" >}}

`daily_digest` writes one `digests` document per UTC day (a unique index on `day` backs it) summarizing the day before, and can be called by hand to (re)build any day.

{{< example-file path="workshop/main/_functions/daily_digest/function.yaml" >}}

{{< example-file path="workshop/main/_functions/daily_digest/index.ts" >}}

To email the digest instead of storing it, see [Calling an outside API](../network/#calling-an-outside-api).

## A payment webhook

{{< live-example path="workshop/#webhook" text="try it in the tour (step 3)" >}}

`payment_webhook` is called by a payment provider, not a user. It verifies the signature of the raw body, ignores an event it already handled (the provider retries), and marks the order paid.

{{< example-file path="workshop/main/_functions/payment_webhook/function.yaml" >}}

{{< example-file path="workshop/main/_functions/payment_webhook/index.ts" >}}

Set its secret with `backd secret set --realm workshop --database main --name PAYMENT_WEBHOOK_SECRET`, and see [Webhooks](../webhooks/) for a `curl` that plays the provider.

## Deliver email with Postmark

`backd` never sends mail itself: the realm names a [delivery function](../email/#the-delivery-function) that hands each finished message to your provider. `postmark` is that function for [Postmark](https://postmarkapp.com), about forty lines, and **any HTTP-API provider is one small function like it**. It lives in the workshop realm's `notifications` database next to `email-capture` (which only stores messages, for development) and is tested without the network.

{{< example-file path="workshop/notifications/_functions/postmark/function.yaml" >}}

{{< example-file path="workshop/notifications/_functions/postmark/index.ts" >}}

### Step by step

1. **At Postmark:** create a *Server* and copy its **Server API token**. Add a *Sender Signature* (a single address) or, better, verify your **domain** (SPF and DKIM), and use an address of it as the sender. Postmark refuses mail from anything it hasn't verified.
2. **Copy the function** into your realm's database (here `notifications/_functions/postmark/`, with a `deno.json` like the one next to it) and run `backd functions build`.
3. **Set its two secrets**, the token and the sender (`POSTMARK_FROM` is the verified sender, such as `Acme <no-reply@acme.example>`; keep `email.from` in `realm.yaml` the same):

   ```sh
   backd secret set --realm acme --database notifications --name POSTMARK_TOKEN --url https://api.example.com
   backd secret set --realm acme --database notifications --name POSTMARK_FROM  --url https://api.example.com
   ```

4. **Point the realm at it** in `realm.yaml`, and remove the development function from the deployment (`dev_only` makes `backd` refuse to start with it outside development):

   ```yaml
   email:
     function: notifications/postmark
     from: "Acme <no-reply@acme.example>"
     public_url: https://api.acme.example
   ```

5. **Try it without sending anything.** Postmark's special token `POSTMARK_API_TEST` makes the API validate a request and answer as if it had sent it, with nothing delivered: set it as `POSTMARK_TOKEN`, sign someone up, and read the email job's result in the [job list](../jobs/). Then set the real token.
6. **Test the function itself** with fake responses, as `index.test.ts` does (`make functions-testing-test`): the request Postmark receives, a refusal, and every kind of failure.

### What `backd` does for you, and what it leaves to you

- **Retries.** A failed attempt (Postmark down, rate limited, the network, a wrong token) is a plain error, so `backd` retries it as `retry` says, and the job list shows each attempt. A message Postmark refuses on its merits (`422`: a malformed or inactive address, an unknown sender) is a `4xx` error from the function, which is **permanent**: no retry.
- **Secrets and links.** The token is a [secret](../secrets/), never in the config or the logs. Links and tokens in the message are masked in the function's logs, and the function can only reach `api.postmarkapp.com`.
- **Duplicates.** Postmark has no idempotency key, so an attempt whose answer was lost and is retried can send the message twice. The function sends the email's id as metadata (`backd_email_id`), so a repeat is visible in Postmark's reports. Providers with a key (Resend's `Idempotency-Key`) can use `ctx.input.id`, which is the same on every retry, to drop it.
- **Deliverability is yours:** domain authentication, bounces, complaints and suppression lists are Postmark's dashboard and your sending reputation, not `backd`'s (see [Email](../email/)).

### Another provider

The shape never changes: build the request from `ctx.input`, send it with `fetch`, return `{ message_id }`, throw `ctx.error(4xx, …)` when the provider refuses the message and a plain error otherwise, declare the key as a secret and the host in `network`. What differs (check the provider's current documentation for the details):

| Provider | Endpoint | Authentication | Notes |
|---|---|---|---|
| Resend | `POST https://api.resend.com/emails` | `Authorization: Bearer <key>` | Send `Idempotency-Key: <ctx.input.id>`: a retry can't duplicate |
| SendGrid | `POST https://api.sendgrid.com/v3/mail/send` | `Authorization: Bearer <key>` | A `202` with an empty body is the success; the message id is in a header |
| Mailgun | `POST https://api.mailgun.net/v3/<domain>/messages` | HTTP basic: `api:<key>` | Form-encoded, not JSON; EU accounts use `api.eu.mailgun.net` |
| Brevo | `POST https://api.brevo.com/v3/smtp/email` | `api-key: <key>` header | |
| Amazon SES | `POST https://email.<region>.amazonaws.com/v2/email/outbound-emails` | AWS Signature V4 | Needs request signing: more code, same contract |

## What to take from these

- **Run as the caller by default** (`ctx.db`); use `admin: true` deliberately, for changes a rule must not allow, and keep those functions small.
- **Make every function that can run twice safe to run twice**: a key derived from the job, a unique index, an idempotency key.
- **Let the data say it's done**: a report document, a status field, an event: clients look where they already look.
- **Test the logic with Deno and the wiring for real**: the workshop has both.
- **An email provider is one small function**: the contract is a finished message in, a message id out, a `4xx` error for what the provider refuses and a plain error for what is worth retrying.
