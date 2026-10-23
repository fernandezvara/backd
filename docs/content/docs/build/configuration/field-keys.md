---
title: "Field keys and encrypted fields"
description: "Encrypt fields before they reach MongoDB, with a key you hold a copy of: creating and saving the key, marking fields, rotating the key instantly, and what encrypted fields can't do."
icon: "key"
weight: 218
toc: true
---

Some fields (a tax id, a health note) must not be readable from a database dump or a backup. `backd` encrypts them before they reach MongoDB and decrypts them for the callers your rules allow, with a key **you** hold a copy of. A **field key** is 32 random bytes, stored by `backd` like any other [secret](../../functions/secrets/) (sealed under `BACKD_SECRETS_KEY`) and used for nothing else: no function, sign-in provider or setting can read it.

{{< hint style="tip" title="Already done for you" >}}
A field key only protects **data keys**, and data keys protect the values. Replacing a key re-wraps a handful of data keys and rewrites no document, so [rotating](#rotate-a-key) is instant whatever the size of the collection.
{{< /hint >}}

## Create a key

```sh
backd secret create --realm clinic --type key --generate --name HEALTH > health.key
# Key HEALTH created, fingerprint HEALTH:9f2c41d0.
# This is the only time the key is shown. Store it somewhere safe outside backd: …
```

The key is printed on standard output **once**, so it can go straight into a file or a password manager; the fingerprint and the warning go to standard error. Nothing ever shows it again: not the API, not the command line, not the admin interface. `backd` keeps its own sealed copy to work with, and **you** keep yours.

{{< hint warning >}}
A key that is lost is a key that is gone. If `backd` also loses its sealed copy (a lost `BACKD_SECRETS_KEY`, a database without its secrets), every value encrypted with it is unrecoverable. Save the key where you save your other irreplaceable credentials, before you use it for anything.
{{< /hint >}}

To use a key you already hold (restoring after a loss, or the same key in a staging realm), import it. The value is read from where you say, so it stays out of your shell history:

```sh
backd secret create --realm clinic --type key --name HEALTH --value file:./health.key
backd secret create --realm clinic --type key --name HEALTH --value env:HEALTH_KEY
```

`--value` takes `key:<base64url>`, `env:<VAR>` or `file:<path>`; a key is 32 bytes written in base64url. Importing never prints the key: it answers with the fingerprint.

A name that is taken, by a key or by any other secret, is refused (`409`): creating a key never overwrites one. Names follow the rule of every secret: upper-case letters, digits and `_`, starting with a letter. Keys live in the realm scope.

## Fingerprints

The fingerprint is the first 8 hex characters of SHA-256 over a fixed label and the key (`HEALTH:9f2c41d0`). It lets you check that a saved copy is the right one without showing the key: create or import, then compare the fingerprint with the one you wrote down. It reveals nothing useful about the key.

`backd secret list` shows each key's fingerprint, and, for a key, the fields that use it:

```
SCOPE  NAME    TYPE    FINGERPRINT  UPDATED                   UPDATED BY      USED BY
realm  HEALTH  key     9f2c41d0     2026-10-22T10:00:00.000Z  user:dbd88k…
realm  STRIPE  secret  -            2026-10-02T10:00:00.000Z  user:dbd88k…
```

## Delete a key

```sh
backd secret delete --realm clinic --name HEALTH
```

A key is refused (`409`) while a field of the configuration names it, **and** while data keys that values were encrypted with are wrapped by it: removing the field from the configuration does not make its stored values plain. Delete only a key you no longer need, because what it protects can't be read without it.

## Who can do it

Creating, importing and deleting a key need a **full administrator** and a fresh proof that it is you (`X-Backd-Reauth`; the command asks for it). A partial administrator, even one holding the `secrets` area, is refused. Listing needs only the `secrets` area, and shows names and fingerprints, never values. Each action is [audited](../../../operate/administration/audit/) as `secret.key.create` or `secret.key.delete`, with the name and the fingerprint, never the key.

A plain `PUT /_admin/secrets/{name}` can't replace a key's value (`409`): a key changes only as a key.

## Mark the fields

`collection.yaml` names the key of each encrypted field. Any declared field can be encrypted, including a path inside an object, and whatever its type: a string, a number, an object or an array is encrypted as a unit.

```yaml
fields:
  tax_id:
    encrypt: { key: TAX }
  health_notes:
    encrypt: { key: HEALTH }
  address.street:         # a nested path
    encrypt: { key: HEALTH }
```

- `schema.json` still describes the **plain** value, and requests are validated against it before the field is encrypted.
- Create the keys first. `backd` checks, when it starts, that each field is declared in `schema.json` and that its key exists and **is a key** (not a plain secret); it refuses to start otherwise, naming the field and the command that creates the key. A realm with `auth: disabled` has nowhere to keep keys, so it can't declare encrypted fields.
- At startup `backd` also gives each field a **data key** to write with. The same field never shares a data key with another, even when they share a field key.
- The data written before a field was encrypted stays readable as it is, and stays plaintext until you [migrate it](#change-encryption-on-existing-data).

What is stored is the encrypted value (BSON binary, about 45 bytes larger than the JSON of the value), bound to its realm, database, collection, field path and document id: a value copied into another document or field fails to decrypt instead of being accepted. A read of such a value answers `500` and logs where, never the value.

## What an encrypted field can't do

A database can't compare what it can't read, so `backd` refuses, **at startup** and naming the file and the field, any configuration that needs the plain value in the database:

- a field in `indexes.json` or in the text index;
- the source of a [reference](../config-dir/#references);
- a [file field](../../files/file-fields/), `_meta` and `id`, a date field (`x-backd-store: date`) and a field inside an array (encrypt the array as a whole);
- a field inside another encrypted field;
- a `replace`, `pull` or `unset` target of `on_owner_delete` (`remove` is fine);
- an `aggregate` group or metric;
- a field used by a **read rule**, unless it has a blind index and the rule only uses `==` or `in` on it. `create`, `update` and `delete` rules read the plain value in memory, so they can use it freely.

A request that filters or sorts by an encrypted field (`where`, `order_by`) answers `400 field_encrypted`, naming the field, unless the field has a [blind index](#search-an-encrypted-field), which answers equality. Who may see a field in the answer is decided by its [`reveal` rule](#who-sees-an-encrypted-field).

Everything that reads documents sees the plain value: the API, `_batch`, functions (`ctx.db`), the admin data views, [triggers](../config-dir/#triggers) and realtime. The documents of a collection that encrypts a field can't be exported or imported yet (`422` naming the collections): the package format has to carry them as ciphertext first, and the server never decrypts for an export.

## Search an encrypted field

By default an encrypted field can't be queried. When you need to look documents up by it, ask for a **blind index**:

```yaml
fields:
  tax_id:
    encrypt:
      key: TAX
      search: equality          # a blind index for = and in
      normalize: [trim, upper]  # applied before the index only: " b1234 " finds "B1234"
      unique: true              # two documents can't hold the same value (409 on a clash)
```

```
GET /v1/clinic/main/patients?where={"tax_id": "B1234"}
GET /v1/clinic/main/patients?where={"tax_id": {"$in": ["B1234", "C99"]}}
```

`backd` stores, next to the encrypted value, a keyed hash (HMAC-SHA-256 under a random index key that the field key wraps, like a data key) of the normalized value, in a hidden indexed field that is never returned or accepted. A query is translated to a lookup of the hash of what you asked for. `normalize` takes `trim`, `lower` and `upper`, in order, and changes nothing but the index: the stored value keeps its case and spaces. `unique: true` needs `search`, because the index is what is unique; a document without the field doesn't clash with another, and with [soft delete](../config-dir/#soft-delete) a deleted document's value can be used again.

**Only equality works** (`=`, `$eq` and `$in`; `null` can't be searched for). Ranges, `$ne`, text operators, sorting, counting distinct values and grouping answer `400 field_encrypted`. A read rule can compare the field the same way, with a value that doesn't come from the document: `document.tax_id == user.tax_id` or `document.code in ['X1', 'X2']`.

{{< hint warning >}}
**What a blind index reveals.** Equal values have equal indexes, so someone holding a dump can tell which documents share a value, though not what the value is. That is fine for values with many possibilities (tax ids, emails, card fingerprints). Do **not** use it for values with few (blood type, yes/no, a country): the groups they form would give them away. Documents written before the index existed aren't found by a search until [a migration](#change-encryption-on-existing-data) builds theirs.
{{< /hint >}}

## Who sees an encrypted field

By default whoever may read a document sees its encrypted fields in plain. A `reveal` rule narrows that, per field, after the read rule has let the document through:

```yaml
fields:
  health_notes:
    encrypt: { key: HEALTH }
    reveal: user != nil && (hasRole(user, 'doctor') || document._meta.owner == user.id)
```

The rule is an [access rule](../../auth/rules/) that sees `document` (all of it, in plain), `user`, `now`, `hasRole`, the organization helpers (`hasOrgRole(document._meta.org, 'admin')`) and `appConfig`; it can't see `data`. Like any rule, guard `user` against anonymous callers. It is checked when `backd` starts, and an error names the field.

For a reader the rule refuses, the field comes back **`null`** and the document lists it:

```json
{ "id": "…", "name": "Ana", "health_notes": null, "_meta": { "redacted": ["health_notes"], … } }
```

so an app can tell *hidden* from *empty* (`null` means empty only when the path is not in `_meta.redacted`). A rule that can't be decided hides the field.

- **Writing needs no reveal.** A caller allowed to update can replace a field they can't read; the answer to their write is their own view. A `PATCH` of another field leaves a hidden field as it was.
- **Rules still read the plain value.** `update` and `delete` rules, and `before`/`after` in [triggers](../config-dir/#triggers), see encrypted fields whether or not the caller may.
- **Expanded documents** apply their own collection's `reveal`; a selection (`expand=patient(name,notes)`) lists in `_meta.redacted` the selected fields it hides.
- **Functions.** `ctx.db` acts as the caller and follows their `reveal`; `ctx.admin.db` sees the plain value. API keys do too, as they pass the rules.
- **Realtime** events carry the same view, evaluated for the caller who subscribed; a change to their access ends the stream.
- **Administrators.** A full administrator reading through the [admin data route](../../../operate/administration/admin/#data) sees every field, and each such read is audited (`data.reveal`: the fields and how many documents, never a value). A read-only administrator who may read data always gets them redacted.
- **Caching.** An answer with hidden fields has a representation tag in its `ETag` (`"12.r3f9a1c2b"`), so a caller whose view changes (a role granted, a document handed over) never gets a stale `304`. `If-Match` still compares the version only.

## Change encryption on existing data

Turning encryption on for a field that already has values, replacing a key, or turning it off must not need a downtime, so **reads accept every form**: plaintext left from before, values under an older data key, and values of a field the configuration no longer encrypts. **Writes always follow the current configuration.** Moving what is already stored is a background job you start when you are ready:

| Situation | What to run | Cost |
|---|---|---|
| Routine replacement of a field key, or a copy of it may have leaked | [`backd secret rotate NAME`](#rotate-a-key) | instant: wraps its data keys again |
| The data keys may have leaked (a server's memory, a decrypted dump) | `backd encrypt rekey --database d --collection c --field f`, then `migrate` | new generation for writes; a job seals the old values again |
| A field moved to another key (`key:` changed in `collection.yaml`) | restart `backd`, then `migrate` | a new data key under the new key; a job seals the old values again |
| A field is now encrypted (or searchable) and has values | `migrate` | a job seals them and builds the indexes |
| A field is no longer encrypted | `migrate` | a job opens the values (the data keys stay until nothing uses them) |

```sh
backd encrypt status  --realm clinic                       # how every field is stored
backd encrypt migrate --realm clinic --database main --collection patients --dry-run
backd encrypt migrate --realm clinic --database main --collection patients
```

`status` counts, per field, the documents holding the value in plaintext, encrypted, sealed with the **current** data key and with an **old** one, how many have their blind index, the key with its fingerprint and the generations of its data keys. A collection is **complete** when nothing is left to migrate; `backd` also warns at startup about every field that is not.

The migration runs on a worker, reads the collection in id order and rewrites only the documents that differ, at most 1,000 a second so the requests that use the collection are not starved. It never changes `_meta.version` or `updated_at`, and [triggers](../config-dir/#triggers) and realtime are not told: the document reads the same before and after. If a worker dies, another goes on from the place the job stored; a document someone else wrote meanwhile is already in the current form and is left alone. `--dry-run` counts and changes nothing; `--field` limits it to some fields (fields no longer encrypted are always opened). It needs a full administrator and a fresh proof, and is audited (`encrypt.start`, `encrypt.migrate`).

- **Searching while it runs.** An equality search finds a document only once its blind index is built, so it can miss documents until the job ends; `status` shows the coverage.
- **Retiring.** When the job ends and no value is left under an old generation (or a field is no longer encrypted and none is left at all), those data keys become `retired`. A retired key still opens a backup you restore; it only stops counting as in use, so the field key can be deleted when no active key is wrapped by it.
- **Before the new configuration.** Run `backd provision` (or start with `PROVISION_MODE=apply`) so the database validator matches the configuration. It expects binary where a field is encrypted; documents already stored the old way stay valid to update.

## Rotate a key

```sh
backd secret rotate --realm clinic --name HEALTH --generate > health.key
# Key HEALTH rotated: HEALTH:3b8e51aa replaces HEALTH:9f2c41d0, 3 data key(s) wrapped again, no document rewritten.
```

The new key is printed once, like a new one. Keep your **old** copy for backups made before the rotation; a dump taken earlier needs the key that was current then. `--value key:|env:|file:` brings a key of your own.

If the command stops before it ends, nothing is lost: the previous value is kept until every data key has been wrapped again. Starting another rotation is refused until you run `backd secret rotate --realm clinic --name HEALTH --resume`. A rotation needs a full administrator and a fresh proof, and is audited as `secret.key.rotate` with both fingerprints.

## The API

`POST /_admin/secrets` creates a key (`{"name", "type": "key", "generate": true}` or `"value": "<base64url>"`); a generated key is in the `201` answer, with `Cache-Control: no-store`, and nowhere else. `POST /_admin/secrets/{name}/rotate` rotates one. The reference is in [Admin → Secrets](../../../operate/administration/admin/#secrets).
