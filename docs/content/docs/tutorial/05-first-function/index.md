---
title: "5. The first function"
description: "publish, share and share-open: what rules can't say, TypeScript on a Deno sandbox says — admin access, invoke rules, idempotency."
weight: 250
toc: true
---

Chapter 3 left two `HOLE` comments: members could write `published_at` themselves, and share links trusted the client's `asset_id` and `token`. Rules can't fix either — "a curator, at the server's now" and "a token nobody chose" need code. This chapter writes it.

## A function is three files

Create `config/shelf/main/_functions/publish/` with three files:

- **`function.yaml`** — the contract: who may call it (`invoke`), what it may do (`admin`, `calls`, `network`, `secrets`), how it runs.

{{< example-file path="shelf/main/_functions/publish/function.yaml" lines="1-11" >}}

- **`input.schema.json`** — validates the request's JSON before the code runs.

{{< example-file path="shelf/main/_functions/publish/input.schema.json" >}}

- **`index.ts`** — the handler: `ctx.input` (validated), `ctx.admin.db("main")` (full access, because `admin: true`), `ctx.error(status, code, message)` for answers the caller understands.

```ts
// publish: a curator publishes a member's draft — published_at becomes the
// server's now, a field rules forbid clients to write.
import { relay } from "../lib/relay.ts";
import type { Context, Doc } from "../lib/types.ts";

export default async function handler(ctx: Context) {
  const { asset_id } = ctx.input as { asset_id: string };
  const db = ctx.admin.db("main");

  let asset: Doc;
  try {
    asset = await db.collection("assets").get(asset_id);
  } catch (err) {
    relay(err, ctx.error);
  }
  if (asset.published_at) {
    throw ctx.error(409, "already_published", `"${asset.title}" is already published`);
  }

  const published_at = new Date().toISOString();
  try {
    await db.collection("assets").patch(asset_id, { published_at });
  } catch (err) {
    relay(err, ctx.error);
  }

  console.log(`published ${asset_id} at ${published_at} (requested by ${ctx.user?.email ?? "api-key"})`);
  return { asset_id, published_at };
}
```

It imports two small helpers every function of the database shares — `lib/relay.ts` (turns a store error into the right `ctx.error`) and `lib/types.ts` — and `_functions/deno.json`; the files below fetch them. (The finished `publish` in the repository also calls `notify`: chapter 7 adds that.)

`invoke` is a rules expression like the collections': `hasRole(user, 'curator')` — API keys always pass. `idempotency: required` means every call must carry an `Idempotency-Key` header, and a retry with the same key replays the first answer instead of publishing twice.

## Fetch the shared files

The helpers, the two other functions of this chapter and their tests (the finished `publish` files are the ones typed above and in chapter 7, so they are not in this list):

{{< tutorial-files "main/_functions/deno.json main/_functions/lib/relay.ts main/_functions/lib/types.ts main/_functions/publish/input.schema.json main/_functions/share/function.yaml main/_functions/share/index.ts main/_functions/share/input.schema.json main/_functions/share/index.test.ts main/_functions/share-open/function.yaml main/_functions/share-open/index.ts main/_functions/share-open/input.schema.json main/_functions/share-open/index.test.ts" >}}

## The dev loop

Functions run as **TypeScript in a Deno process with no permissions** (no disk, no network unless `network:` says so) — one process per call, started by the executor container, so a function can only do what its `function.yaml` and the `ctx` it is handed allow ([how they run](../../functions/writing/)). backd runs the bundled JavaScript, so the sources are bundled before it starts. `functions-build` is the compose service that does it — it uses the executor image, so there is nothing to install. backd refuses to start with stale or missing bundles, so after editing a function you run the build and restart:

```sh
docker compose run --rm functions-build && docker compose restart backd
```

Then call it as the curator (log in as in chapter 3, with `curator@shelf.example`, and use that token): `POST /v1/shelf/main/_func/publish` with `{"asset_id": "…"}` and an `Idempotency-Key` header — or from the client, `db.fn('publish', { asset_id }, { idempotencyKey })`. A function answers errors with the code it chose (`403` for a member, `409 already_published`), never a stack trace.

## Tested without a stack

`index.test.ts` runs under plain Deno — no MongoDB, no executor: `createContext` fakes the handler's context, `MemoryStore` is a throwaway database. The test asserts what the function's caller sees, including failures:

{{< example-file path="shelf/main/_functions/publish/index.test.ts" >}}

Testing is optional, and the tests are plain files in the repository layout, so they import the fake context by a relative path. To run them from your project folder, put the two files it needs where they expect them (a `clients/` folder **next to** your project folder) and run Deno from the executor image you already have — it carries the Deno version backd runs functions with, so nothing needs installing:

```sh
mkdir -p ../clients/functions-testing/src ../clients/js/src
curl -fsSL https://fernandezvara.github.io/backd/tutorial/functions-testing/index.js \
  -o ../clients/functions-testing/src/index.js
curl -fsSL https://fernandezvara.github.io/backd/tutorial/client/errors.js \
  -o ../clients/js/src/errors.js

docker run --rm --entrypoint deno -v "$(dirname "$PWD")":/w:Z -w "/w/$(basename "$PWD")" \
  ghcr.io/fernandezvara/backd-executor:latest test config/shelf/main/_functions/
```

(With Deno installed locally, `deno test config/shelf/main/_functions/` does the same; on a checkout it is `make functions-testing-test`.) The `share` tests pass now; the `publish` test shown is the finished one, which also asserts the `notify` call chapter 7 adds — save it with chapter 7.

## share, and share-open

Two more close the second hole:

{{< example-file path="shelf/main/_functions/share/function.yaml" >}}

`share` checks the asset exists and is published, mints a 24-character token, stamps `created_by` — nothing the client sent decides any of it. `share` also requires an `Idempotency-Key` (a retry must not mint a second link), and it answers `409 not_published` for a draft. `share-open` is the other side: `invoke: "true"` lets strangers call it, `rate_limit` keeps guessing expensive, and it returns a *projection* — title, kind, url, body, tags — never the shares row, never `_meta`.

The collections' rules tighten to match. Replace the `rules:` section of both `collection.yaml` files (the erase policy below it stays):

{{< example-file path="shelf/main/assets/collection.yaml" lines="1-32" >}}

`assets` now refuses `published_at` in client writes — and, since a curator can't publish what they can't see, the `read` rule gains `hasRole(user, 'curator')`: curators read every draft, members still read only their own. And `shares`:

{{< example-file path="shelf/main/shares/collection.yaml" lines="1-10" >}}

has no `create`/`update` rule at all (functions write, nobody else), and `created_by` — not `_meta.owner`, which is the function — scopes who may read or revoke a link. Remember `docker compose restart backd` after the build above.

## What the app does with it

- The new-asset form saves **drafts** — members can no longer set `published_at` (try it: a `POST` with the field answers `403`); publishing is what curators do.
- A curator gets a **Review** view (with a count of waiting drafts): every member's unpublished assets, oldest first, each with **Publish** — one `db.fn('publish', …)` call. A curator also sees **Publish** on their own drafts in My assets.
- **Share** on any published asset mints a link with an expiry; **My active links** lists and revokes them.
- `/?s=<token>` — the link, opened in a private window — shows the asset through `share-open`, no sign-in.

## You should see

- The `share` and `share-open` tests pass (`publish`'s waits for chapter 7).
- `POST …/_func/publish` as a member answers `403`; as `curator@shelf.example`, `200` and the draft gains `published_at`.
- The curator's **Review** view lists a member's draft (a plain member's *My assets* never shows someone else's); publishing it from there moves it to the gallery.
- Two calls with the same `Idempotency-Key` return the same answer; without the header, `400`.
- A share link resolves at `/?s=<token>`; a made-up token answers 404; a member's `POST` on `shares` answers `403`, and a `GET` lists only that member's own links.
- `docker compose logs backd` shows the function's `console.log` line per publish.

Next: chapter 6 — `preview` reaches the network, behind an allowlist.
