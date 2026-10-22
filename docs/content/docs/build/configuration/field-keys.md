---
title: "Field keys"
description: "The keys that protect encrypted fields: creating or importing one, saving it, checking it by fingerprint, and what happens if it is lost."
icon: "key"
weight: 218
toc: true
---

Some fields (a tax id, a health note) must not be readable from a database dump or a backup. `backd` will encrypt them before they reach MongoDB, with a key **you** hold a copy of. This page is about that key. A **field key** is 32 random bytes, stored by `backd` like any other [secret](../../functions/secrets/) (sealed under `BACKD_SECRETS_KEY`) and used for nothing else: no function, sign-in provider or setting can read it.

{{< hint style="note" >}}
Encrypting the fields themselves is planned: `collection.yaml` will name the key per field, and the data will be encrypted with keys wrapped by it. Today a realm can create, import, list and delete keys, and `fields.<path>.encrypt.key` is already checked at startup, so a field never starts without its key.
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

A key that a field of the configuration still names is refused (`409`, naming those fields): change the configuration first. Delete only a key you no longer need, because what it protects can't be read without it.

## Who can do it

Creating, importing and deleting a key need a **full administrator** and a fresh proof that it is you (`X-Backd-Reauth`; the command asks for it). A partial administrator, even one holding the `secrets` area, is refused. Listing needs only the `secrets` area, and shows names and fingerprints, never values. Each action is [audited](../../../operate/administration/audit/) as `secret.key.create` or `secret.key.delete`, with the name and the fingerprint, never the key.

A plain `PUT /_admin/secrets/{name}` can't replace a key's value (`409`): a key changes only as a key.

## Declaring a field's key

`collection.yaml` will name the key of each encrypted field:

```yaml
fields:
  tax_id:
    encrypt:
      key: TAX
```

`backd` checks, when it starts, that the field is declared in `schema.json` and that the key exists and **is a key** (not a plain secret). It refuses to start otherwise, naming the field and the command that creates the key. A realm with `auth: disabled` has nowhere to keep keys, so it can't declare encrypted fields.

## The API

`POST /_admin/secrets` creates a key (`{"name", "type": "key", "generate": true}` or `"value": "<base64url>"`); a generated key is in the `201` answer, with `Cache-Control: no-store`, and nowhere else. The reference is in [Admin → Secrets](../../../operate/administration/admin/#secrets).
