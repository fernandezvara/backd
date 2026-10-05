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

Cron expressions work too (`"0 7 * * *"`, UTC unless you add a `timezone`). A scheduled run has **no caller**: `ctx.user` is `null`, `ctx.input` is `null`, `ctx.db` acts as an anonymous caller — and `ctx.admin.db` is *always* available, recorded as `func:shelf/main/digest`. Review a scheduled function as if it declared `admin: true` ([Cron](../../functions/cron/)).

That's also why `digest`'s input schema accepts `null` and the code defaults `since_days` — the scheduled run sends nothing.

Add that line to `digest/function.yaml` and rebuild. To watch the clock without waiting a day, set `schedule: "* * * * *"` for a minute: `docker compose logs backd` shows `scheduled job queued` and the digest's own `function log` line at the next minute mark — then put `@daily` back. Backd then runs it by itself in the background process (`--with-worker` is in the tutorial's compose file); there is nothing else to start.

## cleanup

Expired share links are litter; notifications read a month ago are stale. Nobody should click "clean" — the clock does it:

{{< example-file path="shelf/main/_functions/cleanup/function.yaml" >}}

`internal: true` + `schedule:` — no HTTP route, no caller, runs at 03:00 UTC. Note `admin:` isn't set: it exists for *HTTP* callers, and this function has none. (The same file is why `mode: async` + `timeout:` show up together — sweeping many documents takes longer than a request should.)

{{< example-file path="shelf/main/_functions/cleanup/index.ts" >}}

{{< tutorial-files "main/_functions/cleanup/function.yaml main/_functions/cleanup/index.ts main/_functions/cleanup/index.test.ts" >}}

## Running by hand

You don't wait for the clock to know it works. The admin API runs any function by hand, internal ones included, as its schedule would — with the operator's `$OP` session from chapter 4 (the CLI form, `backd functions invoke --function shelf/main/cleanup`, does the same from a machine with a stored login):

```sh
curl -s -X POST http://localhost:8080/v1/shelf/_admin/functions/main/cleanup/invoke \
  -H "Authorization: Bearer $OP" -H 'Content-Type: application/json' -d '{}'
# 202 {"id":"…","status":"queued", …}  — a job; poll GET /v1/shelf/main/_jobs/<id> (same $OP) until "status":"done"
```
 The app does the same from the Admin view: `backd.admin.invokeFunction('main/cleanup')`, and its result (or job) under **Run by hand**. That's the second invoke path in this tutorial: `db.fn` for functions with a route and an `invoke` rule; `admin.invokeFunction` for everything else — invoke rules and `rate_limit` don't apply, so guard it to admins in the UI.

## You should see

- The `invoke` call above answers with a job; expired shares and month-old read notifications are gone.
- **Run by hand** in the app runs either function and shows the job or result — `cleanup` included, despite having no HTTP route.
- `docker compose logs backd | grep "function log"` the morning after shows the scheduled digest ran itself.
- `deno test` covers cleanup's keep/delete split.

Next: chapter 10 — email for real: templates, a delivery function, and the digest going out by mail.
