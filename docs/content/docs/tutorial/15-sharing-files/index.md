---
title: "15. Sharing and counting downloads"
description: "A download function that counts and returns the link, share links that open with their files, and the operator's view of the storage."
weight: 350
toc: true
---

Shelf can hold files; this chapter makes them travel. Members will see **how often** an asset's files were downloaded, a stranger holding a share link can download without an account, and the operator can see what it all costs.

## Counting downloads

A plain download link tells `backd` nothing: the browser fetches the file from the storage. To count, the app asks a **function** for the link, and the function counts. That is the whole trick: a function is called with `POST` and answers JSON, so it can't stream a file itself. It returns the link, and the app opens it.

{{< example-file path="shelf/main/_functions/download/index.ts" >}}

Two identities again. The link is asked as **the caller** (`ctx.db`): getting a link is reading the asset, so the asset's `read` rule decides who gets one, and one you can't read is a `404`. The count is written as **the function** (`ctx.admin.db`), in a field the rules refuse to clients, with `ifMatch` so two downloads at once don't lose a count (the loop retries on a version clash).

{{< example-file path="shelf/main/_functions/download/function.yaml" >}}

The input is validated, the app calls it from the gallery's download buttons, and then opens the link:

```js
const { url } = await main.fn('download', { document_id: asset.id, field: file.field, file_id: file.id })
window.location.assign(url)
```

Fetch the files, build and restart:

{{< tutorial-files "main/_functions/download/function.yaml main/_functions/download/index.ts main/_functions/download/index.test.ts main/_functions/download/input.schema.json" >}}

```sh
docker compose run --rm functions-build && docker compose restart backd
```

Download from the gallery: the card says `1 downloads`. (Open the *Gallery*, since it is the one counted: *My assets* downloads are the owner's own and aren't.)

## The hole, and closing it

`downloads` is a field of `schema.json`, so until the rules say otherwise a member can write it: an owner can give their own asset a thousand downloads.

```sh
curl -s -X PATCH "http://localhost:8080/v1/shelf/main/assets/$ASSET" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/merge-patch+json' -d '{"downloads": 1000}'   # 200
```

The same two lines as for `published_at` in chapter 5 close it. Edit `assets/rules.yaml` again; this is the finished file's rule:

{{< example-file path="shelf/main/assets/rules.yaml" lines="25-30" >}}

```sh
docker compose restart backd
```

Repeat the `PATCH`: **`403`**. An owner can no longer set their own count, nor create an asset that starts with one, and the `download` function keeps counting because it writes through `ctx.admin.db`.

## Share links that carry files

Chapter 5's `share-open` answers a stranger with a small projection of the asset, never the whole document. A shared asset's files should come with it. The function makes the links itself (`ctx.admin.db`), **after** the token and the expiry were checked: the share link *is* the permission, so no sign-in is asked and no `read` rule runs.

{{< example-file path="shelf/main/_functions/share-open/index.ts" lines="28-40" >}}

Links carry the name, size and type, never ids. They last as long as `presigned_ttl` (five minutes) — long enough to click, and a stranger can ask again while the share link lives. Fetch, build, restart, then share an asset that has a file, and open the link in a private window:

{{< tutorial-files "main/_functions/share-open/index.ts main/_functions/share-open/index.test.ts" >}}

## The operator's view

*Admin* now shows the **Storage** card, from `GET /_admin/storage`: how the storage is set up (the keys by name only), whether the keys are set and the bucket answers, the totals of bytes and files that documents reference, who holds the most, and what waits to be deleted. The same from the command line:

```sh
docker compose exec backd /backd storage usage --realm shelf
```

Two operations to know before this has real data:

- **Backups.** A MongoDB backup doesn't include files. Back the bucket up at the provider (versioning, replication), and after restoring a database run `backd storage reconcile --realm shelf`: it reports the objects no document references, and with `--delete` removes exactly those. It never touches anything younger than 24 hours or with an upload still in flight.
- **Security.** Links work for whoever holds them until they expire, even if the rules change meanwhile, so keep `presigned_ttl` short on sensitive fields and read [Security of files](../../files/security/).

## You should see

- Each download from the gallery raises the count on the card; the unrelated asset's count is unchanged.
- A `PATCH` of `downloads` answers `200` before the rule and `403` after it, and a create that names one `403`.
- A share link opened by a signed-out visitor lists the asset's files, and a click downloads one.
- *Admin → Storage* shows the bytes and files in use, and `backd storage usage --realm shelf` prints the same.
- `deno test` passes for `download` and `share-open`.

That is the whole of Shelf: a team asset library with members, rules, invitations, functions, email, webhooks, files and an operator's console, all of it from configuration and a few small functions. The last stop is [Running it](../12-running/) if you haven't been through it, and then the [documentation](../../) for everything these chapters only touched.
