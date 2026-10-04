---
title: "5. The first function"
description: "publish, share and share-open: what rules can't say, TypeScript on a Deno sandbox says — admin access, invoke rules, idempotency."
weight: 250
toc: true
---

Chapter 3 left two `HOLE` comments: members could write `published_at` themselves, and share links trusted the client's `asset_id` and `token`. Rules can't fix either — "a curator, at the server's now" and "a token nobody chose" need code. This chapter writes it.

## A function is three files

`config/shelf/main/_functions/publish/`:

- **`function.yaml`** — the contract: who may call it (`invoke`), what it may do (`admin`, `calls`, `network`, `secrets`), how it runs.

{{< example-file path="shelf/main/_functions/publish/function.yaml" >}}

- **`input.schema.json`** — validates the request's JSON before the code runs.

{{< example-file path="shelf/main/_functions/publish/input.schema.json" >}}

- **`index.ts`** — the handler: `ctx.input` (validated), `ctx.admin.db("main")` (full access, because `admin: true`), `ctx.error(status, code, message)` for answers the caller understands.

{{< example-file path="shelf/main/_functions/publish/index.ts" >}}

`invoke` is a rules expression like the collections': `hasRole(user, 'curator')` — API keys always pass. `idempotency: required` means every call must carry an `Idempotency-Key` header, and a retry with the same key replays the first answer instead of publishing twice.

## The dev loop

Functions are bundled before backd starts — a change is one command away:

```sh
docker compose run --rm functions-build && docker compose restart backd
```

Then call it: `POST /v1/shelf/main/_func/publish` with `{"asset_id": "…"}` — or from the client, `db.fn('publish', { asset_id }, { idempotencyKey })`.

## Tested without a stack

`index.test.ts` runs under plain Deno — no MongoDB, no executor: `createContext` fakes the handler's context, `MemoryStore` is a throwaway database. The test asserts what the function's caller sees, including failures:

{{< example-file path="shelf/main/_functions/publish/index.test.ts" >}}

```sh
deno test examples/config/shelf/main/_functions/   # or `make functions-testing-test` on a checkout
```

## share, and share-open

Two more close the second hole:

{{< example-file path="shelf/main/_functions/share/function.yaml" >}}

`share` checks the asset exists and is published, mints a 24-character token, stamps `created_by` — nothing the client sent decides any of it. `share-open` is the other side: `invoke: "true"` lets strangers call it, `rate_limit` keeps guessing expensive, and it returns a *projection* — title, kind, url, body, tags — never the shares row, never `_meta`.

The collections' rules tighten to match: `assets` now refuses `published_at` in client writes, `shares` has no `create`/`update` rule at all (functions write, nobody else), and `created_by` — not `_meta.owner`, which is the function — scopes who may read or revoke a link.

## What the app does with it

- **Save draft** replaces *Save & publish* — members can no longer set `published_at` (try it: a `POST` with the field answers `403`).
- A curator sees **Publish** on drafts in My assets — one `db.fn('publish', …)` call.
- **Share** on any published asset mints a link with an expiry; **My active links** lists and revokes them.
- `/?s=<token>` — the link, opened in a private window — shows the asset through `share-open`, no sign-in.

## You should see

- `deno test` (or `make functions-testing-test`) passes the three functions' tests.
- `POST …/_func/publish` as a member answers `403`; as `curator@shelf.example`, `200` and the draft gains `published_at`.
- Two calls with the same `Idempotency-Key` return the same answer; without the header, `400`.
- A share link resolves at `/?s=<token>`; a made-up token answers 404, and direct `GET`/`POST` on `shares` as a member answers `403`.
- `docker compose logs backd` shows the function's `console.log` line per publish.

Next: chapter 6 — `preview` reaches the network, behind an allowlist.
