---
title: "8. Work that takes time"
description: "digest as an async job — the call answers immediately, the caller polls, retry covers a transient failure."
weight: 280
toc: true
---

Fanning a notification out to every member doesn't belong inside an HTTP request. `mode: async` moves it to a job: the call returns `202` with a job id immediately, the work runs on the executor, and the caller watches it finish.

## The function

{{< example-file path="shelf/main/_functions/digest/function.yaml" >}}

- **`mode: async`** — `db.fn('digest', …)` returns a `Job`, not the output.
- **`retry:`** — a failed attempt is repeated: three tries, the wait doubling between them (`5s`, `10s`, capped at `1m`). A function that fails deterministically (its own `ctx.error`) is *not* retried — only crashes and timeouts.
- **`invoke:`** — only an `admin` user (or an API key) may run it by hand; chapter 9 runs it on a schedule instead.
- **`calls: [notify]`** — the fan-out from chapter 7.

{{< example-file path="shelf/main/_functions/digest/index.ts" >}}

## Members in business data

The digest needs "every member" — but realm users live in the **system database**, which functions can't read (it holds credentials, sessions, tokens). The honest pattern: keep a small `members` directory in business data; the app upserts its own row on sign-in, rules let anyone read it:

{{< example-file path="shelf/main/members/rules.yaml" >}}

## Watching a job

The client's `fn()` detects `mode: async` and returns a `Job`:

```js
const job = await main.fn('digest', { since_days: 7 })
while ((status = await job.status()) !== 'done') await sleep(500)
job.data.result   // { status: 'ok', output: { notified: 4, … } }
```

`job.status()` polls `GET _jobs/{id}` — `queued`, `running`, `done`; `job.wait()` does the loop for you, and `job.data.result` is either `{ status: 'ok', output }` or the failure (`function_error`, `timeout`, …) with its code ([Jobs](../../../functions/jobs/)). The Admin view's **Run by hand** wires exactly this: select `digest`, watch the status flip, read the output.

## You should see

- **Invoke** answers with a job id and `queued → running → done`, then the result JSON.
- Every signed-in member gains a `digest` notification (the app upserts each member on sign-in).
- `deno test` covers the fan-out and the quiet period.
- A `POST _func/digest` as a plain member answers `403` — admin only.

Next: chapter 9 — `schedule:` runs `digest` every morning, and `cleanup` removes expired shares on its own clock.
