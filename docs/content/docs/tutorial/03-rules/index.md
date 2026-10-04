---
title: "3. Who may touch what"
description: "rules.yaml: read, create, update and delete rules per collection — and the hole only a function can close."
weight: 230
toc: true
---

Until now every signed-in member could do anything to anything. `rules.yaml` gives each collection four expressions — `read`, `create`, `update`, `delete` — evaluated per call. Operations without a rule are denied.

## The assets rules

{{< example-file path="shelf/main/assets/rules.yaml" >}}

Three names carry the context: `user` (the caller, or `nil` when anonymous — always check `user != nil` first), `document` (the stored asset), `data` (the asset being written). `changed()` lists the fields a write touches, and `hasRole(user, 'curator')` checks a `realm.yaml` role. The full language is in [Access rules](../../auth/rules/).

## The shares rules

{{< example-file path="shelf/main/shares/rules.yaml" >}}

`read` is deliberately narrow: nobody lists share links — not members, certainly not anonymous callers. Chapter 5 resolves `GET /s/{token}` through a function instead.

## Apply and try to break it

```sh
docker compose restart backd
```

Signed in as one member, in the browser console:

```js
const assets = Alpine.$data(document.querySelector('main')).assets
// …or with the client directly, using your session:
await backd.db('main').collection('assets').patch('other-members-asset-id', { title: 'hacked' })
```

`patch` on somebody else's asset answers `403` — `document._meta.owner == user.id` refuses it. Listing assets shows published ones plus your own drafts; another member's drafts are invisible.

## The hole

Both files carry `HOLE` comments. On `assets`:

- **Any member can publish** — `create` and `update` don't touch `published_at`, so a client can set it, or backdate it.
- Rules decide *who* may write; they can't say "a curator, at the server's now" — that needs `admin: true` code, chapter 5.

On `shares`: `asset_id` is never checked against an asset you own, and `token` is any ≥16-char string — a client could mint `aaaaaaaaaaaaaaaa`. Also closed by the chapter-5 `share` function.

{{< hint note >}}
The `HOLE` comments copy the style of the [expenses example](../../examples/): rules do their best, and the comment marks exactly where a function must take over. Write them in your own rules — they are the design review you'll thank yourself for.
{{< /hint >}}

## Conflict-free edits

The my-assets **Edit** now works, and every write carries `If-Match` with the version it read (`_meta.version` — it increases on every write). Two tabs editing the same asset: the second save answers `412 version_mismatch` and the app says someone wrote first — nobody silently overwrites anybody. See [Documents](../../api/documents/).

## You should see

- Anonymous `GET` on assets answers `401`; a member's `patch` on another's asset answers `403`.
- Your drafts appear under **My assets** but not in the gallery; a published one appears in both.
- **Edit** saves; **Delete** asks and removes.
- Editing the same asset in two tabs: the second save shows the version-mismatch message.

Next: chapter 4 — bring a teammate in through invitations.
