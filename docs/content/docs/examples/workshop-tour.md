---
title: "Workshop tour"
description: "A guided walk through the cookbook's functions, with an inspector that shows every call the page makes."
icon: "explore"
weight: 740
toc: true
---

The [functions cookbook](../../functions/cookbook/) explains each function of the `workshop` realm. The **workshop tour** lets you use them: one page, one panel per feature, and an **inspector** beside it that shows every call the page makes, so you see what the browser sent and what `backd` answered.

{{< live-example path="workshop/" text="open the workshop tour" >}}

It is part of the local stack (`make example`, then <https://localhost:8443/example/workshop/>). The page is plain JavaScript on [`@backd/client`](../../clients/js/), with no build step, in `clients/js/examples/workshop`; the realm is `examples/config/workshop`.

## The inspector

Every call the page makes appears on the right, newest first, grouped by the step of the tour that made it. Click one to see:

- the request and the answer, and the request id (the same one `backd`'s own log and `backd functions logs` use);
- **the function's own log lines** and the time it ran, which `backd` returns in response headers only in [dev mode](../../functions/testing/#dev-mode), which the local stack runs;
- **Copy as curl**: a command that repeats the call, with `$TOKEN` where the session token goes (the page never records the token).

## Who you are

The page signs you in as one of four **demo accounts**, creating it the first time (password `dev-p4ssw0rd!`):

| Account | Role | What it is for |
|---|---|---|
| Ada, Bob | customer | Place orders; ask for each other's orders and get a `404` |
| Staff | `staff` | Refund orders |
| Operator | admin | Set secrets, list jobs, read the history, run functions by hand |

Staff and Operator get their roles from the realm's [`realm.yaml`](../../configuration/realm/#roles) (seed assignments, applied when those emails sign up). These accounts and their fixed password exist for the local stack only: never keep demo users like these in a real realm.

## The tour

1. **A function that reads as the caller** (`order_total`): place an order and ask the server for its total. Ask for it as the other customer: `404`, as if it didn't exist. See [a sync function that runs as the caller](../../functions/cookbook/#a-sync-function-that-runs-as-the-caller).
2. **A privileged, idempotent refund** (`refund`): refund a paid order as staff, send it again with the same key (the same answer, no second refund), with a new key (refused), and twice at once. Watch the receipt job that `refund` queued through an [internal function](../../functions/internal/). See [the refund recipe](../../functions/cookbook/#a-privileged-atomic-idempotent-change).
3. **A webhook from a payment provider** (`payment_webhook`): the page plays the provider, signs the body with HMAC-SHA256 and sends it with no credentials; try a wrong signature and a repeated event. "Set the shared secret" does what the operator does with [`backd secret set`](../../functions/secrets/). See [the webhook recipe](../../functions/cookbook/#a-payment-webhook).
4. **A report in the background** (`export_orders`): start the [async job](../../functions/jobs/), watch it go queued, running, done, and download the CSV. See [the report recipe](../../functions/cookbook/#a-background-report).
5. **Operating it**, as the Operator: the secrets that are set (never their values), the jobs with their attempts and next attempt, the history of every call with where it came from, "run `nightly_cleanup` by hand", and the **tree of calls** of a request, which shows `refund_receipt` under `refund`. See [scheduled functions](../../functions/cron/) and [the nightly cleanup](../../functions/cookbook/#a-nightly-cleanup).
6. **Email verification**: sign up a visitor, find the `verify-email` message in the outbox (the [Mailbox](../../functions/email/#developing-without-a-provider) shows the same), verify the address with the token from its link, and read the visitor again. This realm lets people in before they verify; the [expenses examples](../expenses/) require it.

Every panel has a short **What to notice** box, the code it ran, and links to the docs.

## Check it without a browser

`make workshop-tour` (with `make example` running) walks the same steps with the same client library and checks what each one says should happen: the tax, the `404` for a stranger's order, the signed webhook and its replay, the refund and its receipt, the report, and what the operator sees. It exits `0` when every check passes. It uses the demo accounts and places new orders on every run, so it can run again and again.

{{< hint style="note" >}}
The tour needs the local stack: it relies on dev mode for the functions' logs, on a worker for the jobs, and on demo accounts. Against a production-style deployment the inspector still works, without the function logs, but the demo accounts don't exist.
{{< /hint >}}
