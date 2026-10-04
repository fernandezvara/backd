---
title: "6. Reaching the outside"
description: "preview calls api.microlink.io — functions have no network by default; `network:` is the whole allowlist."
weight: 260
toc: true
---

Functions run in a sandbox with **no network by default** — `fetch` to anywhere is refused before a packet leaves. A function that needs the outside declares its hosts in `function.yaml`, and every request exits through the egress proxy that enforces the list.

## preview

Pasting a link into the new-asset form is a chore; `preview` fetches the page's title and description so the form can prefill itself:

{{< example-file path="shelf/main/_functions/preview/function.yaml" >}}

Two things to notice:

- `network: [api.microlink.io]` is the **whole** allowlist — a fetch to any other host is refused at the egress, whatever the code says ([Outbound network](../../functions/network/)).
- **No `admin:`** — the function touches no collection, so it gets no database access at all. Declare only what the function needs.

{{< example-file path="shelf/main/_functions/preview/index.ts" >}}

The egress does the sandboxing, so the handler is ordinary `fetch`. `ctx.error` answers the caller, so it only takes **4xx** statuses — the function's own crash is `500 function_error`, never yours to emit. `424 preview_failed` says "the dependency failed"; callers shouldn't see transport errors.

## Testing without a network

`network:` lives outside the function, so a unit test can't observe it — what it *can* do is replace `globalThis.fetch` and check the code around the call:

{{< example-file path="shelf/main/_functions/preview/index.test.ts" >}}

## Try it

Create the four files (the listings above; or fetch them) and rebuild:

{{< tutorial-files "main/_functions/preview/function.yaml main/_functions/preview/index.ts main/_functions/preview/input.schema.json main/_functions/preview/index.test.ts" >}}

```sh
docker compose run --rm functions-build && docker compose restart backd
```

The tests run as in chapter 5 (`docker run … deno test config/shelf/main/_functions/preview/`).

In the app, the **New asset** URL field gains a **Preview** button: paste a link, click — title and notes fill from the page's metadata. The client call is `db.fn('preview', { url })`, same as chapter 5.

## You should see

- `deno test` passes the fetch-stub tests.
- **Preview** on `https://fernandezvara.github.io/backd/` fills title and notes.
- A `preview` call for a page that fails answers `424 preview_failed`, not a stack trace.
- **See the allowlist work:** change the `fetch` URL in `preview/index.ts` to another host (say `https://example.com/`), rebuild, restart and call it: the answer is `500 function_failed`, and `docker compose logs backd` shows `NotCapable: Requires net access to "example.com:443"` — the function's code can say anything, the allowlist in `function.yaml` decides. Put it back and rebuild.

Next: chapter 7 — functions calling functions: `notify` becomes an `internal:` building block.
