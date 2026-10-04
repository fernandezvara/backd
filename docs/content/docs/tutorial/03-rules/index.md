---
title: "3. Who may touch what"
description: "rules.yaml: read, create, update and delete rules per collection — and the hole only a function can close."
weight: 230
toc: true
---

Until now every signed-in member could do anything to anything. `rules.yaml` gives each collection four expressions — `read`, `create`, `update`, `delete` — evaluated per call. Operations without a rule are denied.

## The assets rules

Create `config/shelf/main/assets/rules.yaml`:

```yaml
# Everyone in the workspace reads published assets; a member also reads
# their own drafts.
read: >
  user != nil
  && (document.published_at != nil || document._meta.owner == user.id)

# Members write their own assets. HOLE: published_at is the client's word —
# any member can publish, and pick the date.
create: user != nil
update: user != nil && document._meta.owner == user.id
delete: user != nil && document._meta.owner == user.id
```

Three names carry the context: `user` (the caller, or `nil` when anonymous — always check `user != nil` first), `document` (the stored asset), `data` (the asset being written). `changed()` lists the fields a write touches, and `hasRole(user, 'curator')` checks a `realm.yaml` role. The full language is in [Access rules](../../auth/rules/).

## The shares rules

And `config/shelf/main/shares/rules.yaml`:

```yaml
# Share links are private to their creator. The public /s/{token} lookup
# happens through a function (chapter 5) — nobody lists this collection.
read: user != nil && document._meta.owner == user.id

# HOLE: asset_id is the client's word — nothing checks the caller owns the
# asset or that it exists; and a token is any string of 16+ characters, so a
# client could pick a guessable one.
create: user != nil
delete: user != nil && document._meta.owner == user.id
```

`read` is deliberately narrow: nobody lists share links — not members, certainly not anonymous callers. Chapter 5 resolves `GET /s/{token}` through a function instead. (These are the chapter's versions; chapter 5 replaces both files. The repository's `rules.yaml` files are their final state.)

## Apply and try to break it

```sh
docker compose restart backd
```

You need a second member to be the victim. In a private window sign up as another address (say `ana@shelf.example` — the realm is still open) and create an asset; or sign in as the member you created in chapter 2 and use the curator as the "other" user. Then, as one member, call the API with your session. A session is a token: sign in by hand and keep it in a variable —

```sh
TOKEN=$(curl -s http://localhost:8080/v1/shelf/_auth/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"<your member address>","password":"<your password>"}' | sed 's/.*"token":"\([^"]*\)".*/\1/')

# the other member's asset id: copy it from the app, or list as them
curl -i -X PATCH http://localhost:8080/v1/shelf/main/assets/<their-asset-id> \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/merge-patch+json' \
  -d '{"title":"hacked"}'
```

The `PATCH` answers `403` — `document._meta.owner == user.id` refuses it. Listing assets shows published ones plus your own drafts; another member's drafts are invisible.

## The hole

Both files carry `HOLE` comments (a comment that marks exactly where a rule can't say what you mean). On `assets`:

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
