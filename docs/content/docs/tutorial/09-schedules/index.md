---
title: "9. On a schedule"
description: "schedule: cron runs a function with nobody calling — ctx.admin.db always, no user, no input. cleanup sweeps expired shares."
weight: 290
toc: true
---

Some work shouldn't wait for a person: the morning digest, the nightly sweep. One line in `function.yaml` and the function runs itself.

## schedule

```yaml
# digest/function.yaml — the same function as chapter 8, plus:
schedule: "@daily"
```

Cron expressions work too (`"0 7 * * *"`, always UTC). A scheduled run has **no caller**: `ctx.user` is `null`, `ctx.input` is `null`, `ctx.db` acts as an anonymous caller — and `ctx.admin.db` is *always* available, recorded as `func:shelf/main/digest`. Review a scheduled function as if it declared `admin: true` ([Cron](../../functions/cron/)).

That's also why `digest`'s input schema accepts `null` and the code defaults `since_days` — the scheduled run sends nothing.

## cleanup

Expired share links are litter; notifications read a month ago are stale. Nobody should click "clean" — the clock does it:

{{< example-file path="shelf/main/_functions/cleanup/function.yaml" >}}

`internal: true` + `schedule:` — no HTTP route, no caller, runs at 03:00 UTC. Note `admin:` isn't set: it exists for *HTTP* callers, and this function has none. (The same file is why `mode: async` + `timeout:` show up together — sweeping many documents takes longer than a request should.)

{{< example-file path="shelf/main/_functions/cleanup/index.ts" >}}

## Running by hand

You don't wait for the clock to know it works: `backd functions invoke --function shelf/main/cleanup` runs an internal function as its schedule would — the answer is a job to watch. The app does the same from the Admin view: `backd.admin.invokeFunction('main/cleanup')`, and its result (or job) under **Run by hand**. That's the second invoke path in this tutorial: `db.fn` for functions with a route and an `invoke` rule; `admin.invokeFunction` for everything else — invoke rules and `rate_limit` don't apply, so guard it to admins in the UI.

## You should see

- `backd functions invoke --function shelf/main/cleanup` answers with a job; expired shares and month-old read notifications are gone.
- **Run by hand** in the app runs either function and shows the job or result — `cleanup` included, despite having no HTTP route.
- `docker compose logs backd | grep "function log"` the morning after shows the scheduled digest ran itself.
- `deno test` covers cleanup's keep/delete split.

Next: chapter 10 — email for real: templates, a delivery function, and the digest going out by mail.
