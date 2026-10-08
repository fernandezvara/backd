---
title: "8. Work that takes time"
description: "digest as an async job — the call answers immediately, the caller polls, retry covers a transient failure."
weight: 280
toc: true
---

Fanning a notification out to every member doesn't belong inside an HTTP request. `mode: async` moves it to a job: the call returns `202` with a job id immediately, the work runs on the executor, and the caller watches it finish.

## The function

Create `config/shelf/main/_functions/digest/`. This chapter's `function.yaml`:

```yaml
mode: async
admin: true
invoke: "user != nil && hasRole(user, 'admin')"
calls: [notify]
retry:
  attempts: 3
  backoff: 5s
  max_backoff: 1m
```

(The repository's file also has `schedule:` and `email: true` — chapters 9 and 10 add them.)

- **`mode: async`** — `db.fn('digest', …)` returns a `Job`, not the output.
- **`retry:`** — a failed attempt is repeated: three tries, the wait doubling between them (`5s`, `10s`, capped at `1m`). A function that fails deterministically (its own `ctx.error`) is *not* retried — only crashes and timeouts.
- **`invoke:`** — only an `admin` user (or an API key) may run it by hand; chapter 9 runs it on a schedule instead.
- **`calls: [notify]`** — the fan-out from chapter 7.

```ts
import type { Context, Doc } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const sinceDays = (ctx.input as { since_days?: number } | null)?.since_days ?? 7;
  const db = ctx.admin.db("main");
  const since = new Date(Date.now() - sinceDays * 86_400_000).toISOString();

  const page = await db.collection("assets").list({
    where: { published_at: { $gte: since } },
    orderBy: "published_at",
    limit: 100,
  });
  if (page.items.length === 0) {
    return { since_days: sinceDays, notified: 0, skipped: true };
  }

  const titles = page.items.map((a) => `“${a.title}”`).join(", ");
  const text = `${page.items.length} published since ${since.slice(0, 10)}: ${titles}`;

  let notified = 0;
  for await (const member of db.collection("members").iterate()) {
    await ctx.call("notify", { to_user: member.user_id, kind: "digest", text });
    notified++;
  }
  console.log(`digest: ${page.items.length} assets, ${notified} members notified`);
  return { since_days: sinceDays, assets: page.items.length, notified, skipped: false };
}
```

`input.schema.json` and the `members` collection (next section) come from the fetch below. The `digest` test in the repository also asserts the emails of chapter 10, so it passes after that chapter.

## Members in business data

The digest needs "every member" — but realm users live in the **system database**, which functions can't read (it holds credentials, sessions, tokens). The honest pattern: keep a small `members` directory in business data; the app upserts its own row on sign-in, rules let anyone read it:

{{< example-file path="shelf/main/members/collection.yaml" >}}

{{< tutorial-files "main/_functions/digest/input.schema.json main/members/collection.yaml main/members/schema.json main/members/indexes.json" >}}

Rebuild and restart (`docker compose run --rm functions-build && docker compose restart backd`).

## Watching a job

The client's `fn()` detects `mode: async` and returns a `Job`:

```js
const job = await main.fn('digest', { since_days: 7 })
while ((status = await job.status()) !== 'done') await sleep(500)
job.data.result   // { status: 'ok', output: { notified: 4, … } }
```

`await job.wait()` returns the function's output once the job is done; `job.status()` polls `GET _jobs/{id}` — `queued`, `running`, `done`; `job.wait()` does the loop for you, and `job.data.result` is either `{ status: 'ok', output }` or the failure (`function_error`, `timeout`, …) with its code ([Jobs](../../functions/jobs/)). The Admin view's **Run by hand** wires exactly this: select `digest`, watch the status flip, read the output.

Note that `members` fills as people **sign in** with the new app code: sign in once with each of your three accounts now (the accounts of chapter 2 have no row yet), or the digest has nobody to notify. To run it without the app, as the operator:

```sh
curl -s -X POST http://localhost:8080/v1/shelf/main/_func/digest \
  -H "Authorization: Bearer $OP" -H 'Content-Type: application/json' -d '{"since_days": 7}'
# 202 {"id":"…","status":"queued"}  — poll GET /v1/shelf/main/_jobs/<id> with the same header
```

## You should see

- **Invoke** answers with a job id and `queued → running → done`, then the result JSON.
- Every signed-in member gains a `digest` notification (the app upserts each member on sign-in).
- `deno test` covers the fan-out and the quiet period.
- A `POST _func/digest` as a plain member answers `403` — admin only.

Next: chapter 9 — `schedule:` runs `digest` every morning, and `cleanup` removes expired shares on its own clock.
