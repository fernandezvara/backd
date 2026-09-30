---
title: "Quickstart"
description: "Your first function, from an empty directory to a call, its logs and a unit test, in about ten minutes."
icon: "rocket_launch"
weight: 551
toc: true
---

You will create a config repository with a working sample function, run it locally, call it three ways, write your own, see what it did, and test it. You need Docker (or Podman) and the `backd` binary; Deno is optional, because the build can run in Deno's own image.

## 1. Create a project

```sh
backd template project --dir ./shop --realm shop
cd shop
```

This writes a complete repository: the `shop` realm with a sample database, a sample function with a test, Dockerfiles, a `compose.yaml` for local development (MongoDB, `backd`, the functions executor and the egress proxy) and a CI workflow. Everything you need is in the generated `README.md`; the parts below follow it.

The function lives here:

```
config/shop/main/_functions/
  deno.json
  stats/
    function.yaml     how it runs
    index.ts          what it does
    index.test.ts     its unit test
```

## 2. Build it and start the stack

`backd` runs *bundles*, so build after every change to a function (it refuses to start with stale ones):

```sh
export EXECUTOR_TOKEN=$(openssl rand -base64 32)
export CALLBACK_KEY=$(openssl rand -base64 32)
export EGRESS_KEY=$(openssl rand -base64 32)

# With Deno 2.9.7 and backd installed:
CONFIG_DIR=./config backd functions build
# Or in Deno's image, without installing Deno:
docker run --rm -v "$PWD/config:/config" -v "$(command -v backd):/usr/local/bin/backd:ro" \
  -e CONFIG_DIR=/config docker.io/denoland/deno:alpine-2.9.7 backd functions build

docker compose up --build
```

`backd` now listens on <http://127.0.0.1:8080>. The three secrets are shared between `backd`, the executor and the egress proxy; the [Running in production](../running/) page explains each.

## 3. Call it

The sample function, `stats`, is public and read-only, so you can call it without signing in:

```sh
curl -X POST http://127.0.0.1:8080/v1/shop/main/_func/stats
# → {"published":0,"mine":0}
```

Three ways to make the same call:

```sh
# HTTP, as above.

# The command line, through the same route any caller uses:
backd functions invoke --function shop/main/stats --url http://127.0.0.1:8080

# The JavaScript client:
#   const stats = await backd.db('main').fn('stats')
```

## 4. Write your own

Create a function and edit it:

```sh
CONFIG_DIR=./config backd template function --realm shop --database main --name hello
```

Replace `config/shop/main/_functions/hello/index.ts` with:

```ts
type Context = {
  input: { name?: string } | null;
  error: (status: number, code: string, message: string) => Error;
};

export default async function handler(ctx: Context) {
  const name = ctx.input?.name;
  if (!name) throw ctx.error(400, "name_required", "Send {\"name\": \"…\"}");
  console.log(`greeting ${name}`);
  return { hello: name };
}
```

Then open `function.yaml` in the same folder. By default only [API keys](../../auth/api-keys/) may call a function; for this tutorial let anyone call it (use `invoke: "user != nil"` for signed-in users only, see [the invoke rules](../reference/#invoke-rules)):

```yaml
invoke: "true"
```

Rebuild, restart `backd` (it reads the bundles at startup), and call it:

```sh
CONFIG_DIR=./config backd functions build
docker compose up -d --build backd

curl -X POST http://127.0.0.1:8080/v1/shop/main/_func/hello -H 'Content-Type: application/json' -d '{"name": "Ada"}'
# → {"hello":"Ada"}
curl -X POST http://127.0.0.1:8080/v1/shop/main/_func/hello -H 'Content-Type: application/json' -d '{}'
# → 400 {"error": {"code": "name_required", "message": "Send {\"name\": \"…\"}", ...}}
```

While you edit function code, run `backd` locally with `BACKD_DEV=true` instead: it rebuilds function sources as you save, with no restart ([Dev mode](../testing/#dev-mode)).

## 5. See what happened

Create the first administrator, sign in, and read the function's history and logs:

```sh
docker compose exec backd /backd bootstrap --realm shop --email ops@example.com
backd login --realm shop --url http://127.0.0.1:8080 --email ops@example.com

backd functions history --function shop/main/hello
# TIME                      STATUS  CODE  DURATION  ACTOR      REQUEST-ID
# 2026-09-30T09:12:41.002Z  ok      -     3ms       anonymous  darf9mq5mh5g00aq9df0

backd functions logs --function shop/main/hello
# 2026-09-30T09:12:41.002Z darf9mq5mh5g00aq9df0 [log] greeting Ada
```

History never contains a call's input or output; only what happened. See [Function logs](../logs/).

## 6. Test it without any of this

`deno test` runs a function's own logic against a fake `ctx`: no MongoDB, no `backd`, no network.

```sh
deno test config/shop/main/_functions/
```

The sample `stats/index.test.ts` shows the pattern. See [Build, dev and test](../testing/#unit-testing-a-functions-logic).

## Where next

- Read data and change it with the caller's permissions: [Writing a function](../writing/).
- Do slow work in the background, and know when it's done: [Async jobs](../jobs/).
- Run something every night: [Scheduled functions](../cron/). Note that async jobs and cron need a **worker**: in the generated `compose.yaml`, `backd` already runs one (`serve --with-worker`).
- Complete recipes: the [Cookbook](../cookbook/).
