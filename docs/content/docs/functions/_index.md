---
title: "Functions"
description: "Run server-side logic next to your data: sync calls, background jobs, cron schedules and webhooks, with the rules, secrets and limits that keep them safe."
icon: "function"
weight: 550
toc: true
aliases: ["/docs/configuration/functions/"]
---

Server-side functions run code you write on the server, next to your collections, for the things a client must not do or be trusted with: computing what readers may not compute themselves, making privileged changes, calling payment or email services with a secret, working on a schedule, answering another service's callbacks. Each function is a small JavaScript or TypeScript module in your `CONFIG_DIR`, next to the collections it works on. `backd` runs it in [Deno](https://deno.com/), in its own process, with only the permissions and limits it declares.

They are a core part of `backd`, not an add-on: a function reaches data through the same [access rules](../auth/rules/) as any other caller (or, when you say so, with full access), and everything around it is built in: input and output schemas, per-caller rate limits, idempotent retries, encrypted secrets, a network allowlist, background jobs, cron schedules, invocation history and console logs.

## What a function looks like

A function is a folder with a `function.yaml` (how it runs) and an `index.ts` (what it does). This one prices one of the caller's orders. It runs *as the caller*, so the orders' access rules decide which orders they can read:

{{< example-file path="workshop/main/_functions/order_total/function.yaml" >}}

{{< example-file path="workshop/main/_functions/order_total/index.ts" >}}

```sh
curl -X POST https://api.example.com/v1/workshop/main/_func/order_total \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"order_id": "d3c9ljp8hc2g00b6s1m0"}'
# → 200 {"order_id": "d3c9ljp8hc2g00b6s1m0", "subtotal": 1000, "tax": 200, "total": 1200}
```

This function, and every other one quoted in this section, is in [`examples/config/workshop`](https://github.com/fernandezvara/backd/tree/main/examples/config/workshop) in the repository, where it is tested for real; the [Cookbook](cookbook/) walks through them all.

## Which kind of function do I need?

| You need to… | Use | Page |
|---|---|---|
| Decide who may read or write which documents | [Access rules](../auth/rules/) — no function needed | |
| Compute something readers can't, or check across several documents | A **`sync`** function | [Writing](writing/), [Calling](calling/) |
| Make a change no user rule may allow (a refund, a role change) | A `sync` function with `admin: true` | [Writing](writing/#privileged-changes-admin-and-batch) |
| Do work that takes more than a few seconds, or must survive the caller going away | An **`async`** job | [Async jobs](jobs/) |
| Keep a function away from HTTP, or share a helper between functions | An **internal** function, called with `ctx.call` | [Internal functions](internal/) |
| Run something every night, hour or week | A **scheduled** function (`schedule:` on an async function) | [Scheduled functions](cron/) |
| Receive a callback from Stripe, GitHub or any other service | A **`webhook`** function | [Webhooks](webhooks/) |
| Call an outside API with a key | Any function, with [secrets](secrets/) and a [network allowlist](network/) | |

## The three modes, and cron

| | `sync` (default) | `async` | `webhook` | scheduled |
|---|---|---|---|---|
| Who calls it | a client, over HTTP | a client, over HTTP | another service | a worker, on a cron schedule |
| The caller gets | the output, right away | `202` and a **job** to check later | the function's own response | nothing (there is no caller) |
| Runs in | the request | a background **worker** | the request | a background worker |
| Time limit | up to 60 s (10 s default) | up to 24 h (15 min default) | up to 60 s | up to 24 h |
| Acts as | the caller (`ctx.db`), or the function (`ctx.admin.db`) | the caller who queued it | nobody: it verifies the sender itself | the function, with full access |
| Delivery | once | at least once | as often as the sender retries | once per scheduled time |

## How it fits together

```
 client ── HTTP ──▶ backd ── runs the function in ──▶ executor ─▶ one Deno process per call
                     │  ▲                                              │
                     │  └────── ctx.db / ctx.admin.db, over an ◀───────┘
                     │           internal listener (short-lived credentials)
                     │
              worker (async jobs and cron schedules) ── also uses the executor
                                                               │  network: only what the function
                                                               ▼  declares, through backd egress
```

- **`backd`** authenticates the caller, applies the function's `invoke` rule, validates input and output, and answers.
- **The executor** starts one Deno process per call and stops it at its deadline. It never sees `CONFIG_DIR`, MongoDB or the signing keys.
- **A worker** (`backd worker`, or `backd serve --with-worker`) claims async jobs and creates the runs of scheduled functions. Async jobs and cron only happen while at least one worker runs.
- **`backd egress`** is the only way a function reaches the outside network, and only the hosts it declares.

## What `backd` enforces for you

- The caller's rules apply to `ctx.db`; full access needs an explicit `admin: true` (or is the point of a scheduled run).
- Time, memory, output size and concurrency limits per function, and a per-caller rate limit you can declare.
- Input and output JSON Schemas, checked on every call.
- Idempotency keys, so a retried request doesn't do the work twice.
- Secrets encrypted at rest and delivered as `ctx.secrets`, masked in logs.
- No environment variables, no files but the function's bundle, no processes, and a network limited to a declared allowlist.
- A record of every call (never its input or output) and the function's own console output, kept for a week by default.

## Where to go next

- **New here?** Start with the [Quickstart](quickstart/), then [Writing a function](writing/).
- **Calling functions:** [Calling functions](calling/) covers HTTP, the JavaScript client and the CLI, and the answers you can get.
- **Background work:** [Async jobs](jobs/) (including how to know a job finished) and [Scheduled functions](cron/) (when to use cron).
- **Recipes:** the [Cookbook](cookbook/) has six complete, tested examples.
- **Reference:** [`function.yaml`](reference/), [Secrets](secrets/), [Outbound network](network/), [Build, dev and test](testing/), [Function logs](logs/).
- **Operating it:** [Running in production](running/), and the [production reference deployment](../operations/production/#functions-executor-egress-and-worker).
- **Worked examples with commentary:** the [blog example](../examples/blog/#a-server-side-function-stats)'s `stats` function is the smallest possible one; [Expenses with functions](../examples/expenses-with-functions/) is a complete small app built around five functions, compared side by side with a version that doesn't use them.
