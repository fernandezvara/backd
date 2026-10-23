---
title: "Recipes"
description: "Encrypt a tax id and look people up by it, show health notes only to doctors, turn encryption on for a field that already has data, and seed staging without exposing the keys."
icon: "key"
weight: 10
toc: true
---

Each recipe is complete: the commands and the configuration in order. The rules of the game are in [Field keys and encrypted fields](../../configuration/field-keys/).

## A tax id you can look up

You need the tax id unreadable in a dump, and still need `GET …?where={"tax_id": "B1234"}` and one patient per tax id.

```sh
backd secret create --realm clinic --type key --generate --name TAX > tax.key    # save tax.key; the fingerprint is printed
```

```yaml
# main/patients/collection.yaml
fields:
  tax_id:
    encrypt:
      key: TAX
      search: equality          # = and in, through a blind index
      normalize: [trim, upper]  # " b1234 " finds "B1234"
      unique: true              # a second patient with the same tax id is a 409
```

Restart `backd` (it refuses to start if the key is missing). Then:

```
GET /v1/clinic/main/patients?where={"tax_id": "B1234"}
POST /v1/clinic/main/patients     {"name": "Ana", "tax_id": " b1234 "}        → 201; the same again → 409
```

Ranges, `$ne`, text operators and sorting on `tax_id` answer `400 field_encrypted`. A tax id has many possible values, which is what makes a blind index safe: **don't** do this for blood type, a yes/no or a country.

## Health notes only doctors read

Everybody who can read a patient may see the name; only doctors, and the person who wrote the notes, see the notes.

```yaml
fields:
  health_notes:
    encrypt: { key: HEALTH }
    reveal: >
      user != nil && (hasRole(user, 'doctor') || document._meta.owner == user.id)
```

Anyone else gets `"health_notes": null` and `"_meta": {"redacted": ["health_notes"]}`. They can still replace the notes with a `PATCH` if the `update` rule lets them (writing needs no reveal). In an organization, `hasOrgRole(document._meta.org, 'admin')` makes the team's admins the readers instead. Functions with `ctx.db` see what their caller sees; `ctx.admin.db` sees the plain value.

## Turn encryption on for a field that already has data

1. Create and **save** the key: `backd secret create --realm clinic --type key --generate --name HEALTH`.
2. Add `fields.<path>.encrypt` to `collection.yaml`.
3. `backd provision` (or start with `PROVISION_MODE=apply`): the database validator now expects binary there. Documents already stored stay readable, in plaintext.
4. Check what would change: `backd encrypt migrate --realm clinic --database main --collection patients --dry-run`.
5. Run it: `backd encrypt migrate --realm clinic --database main --collection patients`. It works in the background, at most 1,000 documents a second, and never changes a version.
6. `backd encrypt status --realm clinic` says **complete** when no plaintext (and, for a searchable field, no missing index) is left.

Until step 5 ends, values written before are plaintext in the database, and an equality search can miss documents whose index is not built yet.

## Move a field to another key, or turn it off

- **Another key:** create the new key, change `key:` in `collection.yaml`, restart (a new generation starts under the new key), then `backd encrypt migrate`. Old values stay readable meanwhile. When it ends the old key's data keys are retired and the old field key can be deleted.
- **Turn it off:** remove `encrypt` from the field, `backd provision`, restart, `backd encrypt migrate`: values are opened and stored as plain text. Reads work throughout.

## Seed staging from production

An anonymized export keeps encrypted fields as ciphertext, so the copy never holds the values in the clear:

```sh
backd export --realm clinic --anonymize --out ./clinic-staging
backd import --realm clinic --from ./clinic-staging          # on staging
```

If staging holds the **same key**, it opens the values itself and seals them again under its own keys. If it has its own keys, give the production keys to the import (`--field 'tax_id=env:PROD_TAX_KEY'`), or decide that staging doesn't need the values and leave those fields empty with the override file's `remove`. The dry run lists which fields need which key. See [Encrypted fields in export and import](../../../operate/administration/export-import/encrypted-fields/#importing).

## After a suspected leak

[If credentials leak → field keys](../../../operate/if-credentials-leak/#5-field-keys) says which of `rotate`, `rekey` and `migrate` to run for a leaked key, leaked data keys and a leaked master key.
