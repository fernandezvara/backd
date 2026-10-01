---
title: "Cookbook"
description: "Six complete, tested recipes: a sync function as the caller, a privileged idempotent refund, a background report, a nightly cleanup, a daily digest and a payment webhook."
icon: "integration_instructions"
weight: 565
toc: true
---

Every recipe here is a function of the `workshop` realm ([`examples/config/workshop`](https://github.com/fernandezvara/backd/tree/main/examples/config/workshop) in the repository): a tiny shop with `orders`, `refunds`, `reports`, `digests` and `events`. The code on this page is read straight from those files, and `TestWorkshopExample` runs every function for real (MongoDB, a real executor, a real worker), so what you read is what runs.

## Run the workshop

The repository's local stack serves the realm, and its `backd` runs a worker (`serve --with-worker`), so jobs and schedules work:

```sh
make example                # docker compose up --build; the API is at https://localhost:8443
docker compose exec backd /backd bootstrap --realm workshop --email ops@example.com   # first administrator

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

`nightly_cleanup` runs at 03:00 UTC and deletes draft orders nobody finished in 30 days, in batches of a hundred, using `ctx.admin.db` (a scheduled run has no caller). It is [internal](../internal/): nobody can call it over HTTP. It only deletes what is already past the cutoff, so running twice is harmless.

{{< example-file path="workshop/main/_functions/nightly_cleanup/function.yaml" >}}

{{< example-file path="workshop/main/_functions/nightly_cleanup/index.ts" >}}

```sh
backd functions jobs --function workshop/main/nightly_cleanup --scheduled --since 7d
```

More in [Scheduled functions](../cron/).

## A daily digest on a schedule

`daily_digest` writes one `digests` document per UTC day (a unique index on `day` backs it) summarizing the day before, and can be called by hand to (re)build any day.

{{< example-file path="workshop/main/_functions/daily_digest/function.yaml" >}}

{{< example-file path="workshop/main/_functions/daily_digest/index.ts" >}}

To email the digest instead of storing it, see [Calling an outside API](../network/#calling-an-outside-api).

## A payment webhook

`payment_webhook` is called by a payment provider, not a user. It verifies the signature of the raw body, ignores an event it already handled (the provider retries), and marks the order paid.

{{< example-file path="workshop/main/_functions/payment_webhook/function.yaml" >}}

{{< example-file path="workshop/main/_functions/payment_webhook/index.ts" >}}

Set its secret with `backd secret set --realm workshop --database main --name PAYMENT_WEBHOOK_SECRET`, and see [Webhooks](../webhooks/) for a `curl` that plays the provider.

## What to take from these

- **Run as the caller by default** (`ctx.db`); use `admin: true` deliberately, for changes a rule must not allow, and keep those functions small.
- **Make every function that can run twice safe to run twice**: a key derived from the job, a unique index, an idempotency key.
- **Let the data say it's done**: a report document, a status field, an event: clients look where they already look.
- **Test the logic with Deno and the wiring for real**: the workshop has both.
