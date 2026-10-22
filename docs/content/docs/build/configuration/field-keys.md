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
- The data written before a field was encrypted stays readable as it is. Encrypting it needs the migration that comes with a later release; until then, only values written from now on are encrypted.

What is stored is the encrypted value (BSON binary, about 45 bytes larger than the JSON of the value), bound to its realm, database, collection, field path and document id: a value copied into another document or field fails to decrypt instead of being accepted. A read of such a value answers `500` and logs where, never the value.

## What an encrypted field can't do

A database can't compare what it can't read, so `backd` refuses, **at startup** and naming the file and the field, any configuration that needs the plain value in the database:

- a field in `indexes.json` or in the text index;
- the source of a [reference](../config-dir/#references);
- a [file field](../../files/file-fields/), `_meta` and `id`, a date field (`x-backd-store: date`) and a field inside an array (encrypt the array as a whole);
- a field inside another encrypted field;
- a `replace`, `pull` or `unset` target of `on_owner_delete` (`remove` is fine);
- an `aggregate` group or metric;
- a field used by a **read rule**. `create`, `update` and `delete` rules read the plain value in memory, so they can use it.

A request that filters or sorts by an encrypted field (`where`, `order_by`) answers `400 field_encrypted`, naming the field. Searching by an encrypted field, rules on it and who may see it in the answer are planned.

Everything that reads documents sees the plain value: the API, `_batch`, functions (`ctx.db`), the admin data views, [triggers](../config-dir/#triggers) and realtime. The documents of a collection that encrypts a field can't be exported or imported yet (`422` naming the collections): the package format has to carry them as ciphertext first, and the server never decrypts for an export.

## Rotate a key

```sh
backd secret rotate --realm clinic --name HEALTH --generate > health.key
# Key HEALTH rotated: HEALTH:3b8e51aa replaces HEALTH:9f2c41d0, 3 data key(s) wrapped again, no document rewritten.
```

The new key is printed once, like a new one. Keep your **old** copy for backups made before the rotation; a dump taken earlier needs the key that was current then. `--value key:|env:|file:` brings a key of your own.

If the command stops before it ends, nothing is lost: the previous value is kept until every data key has been wrapped again. Starting another rotation is refused until you run `backd secret rotate --realm clinic --name HEALTH --resume`. A rotation needs a full administrator and a fresh proof, and is audited as `secret.key.rotate` with both fingerprints.

## The API

`POST /_admin/secrets` creates a key (`{"name", "type": "key", "generate": true}` or `"value": "<base64url>"`); a generated key is in the `201` answer, with `Cache-Control: no-store`, and nowhere else. `POST /_admin/secrets/{name}/rotate` rotates one. The reference is in [Admin → Secrets](../../../operate/administration/admin/#secrets).
