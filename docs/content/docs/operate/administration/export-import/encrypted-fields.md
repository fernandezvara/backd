---
title: "Encrypted fields"
description: "How encrypted fields travel in an export: always as ciphertext, opened only by whoever holds the keys, offline if you like; what an import does with them, and how to recover after losing a realm's secrets."
icon: "lock"
weight: 25
toc: true
---

[Encrypted fields](../../../../build/configuration/field-keys/) are there so that a dump, a backup or a package can't be read without their keys. An export keeps that promise: **the server never decrypts for an export.** Whatever you can read of an encrypted field in a package, you read because **you** supplied its key, in the command on your machine.

## What a package holds

```
backd export --realm clinic --out ./clinic
```

Every encrypted value is in the package as it is stored (ciphertext), with `_system/data_keys.jsonl`, the wrapped data and index keys of those fields, and the manifest lists each field with the **name and fingerprint of its key** and its form:

```json
"encrypted_fields": [
  {"database": "main", "collection": "patients", "path": "health_notes", "key": "HEALTH", "fingerprint": "9f2c41d0", "form": "ciphertext"}
]
```

The wrapped keys are useless without the field keys, which never travel, so the package is safe to move where a dump would be. [Anonymized](../seeding-staging/) copies and [reporting extracts](../reporting-extracts/) keep encrypted fields as ciphertext too (a CSV cell shows it as base64).

## Opening fields

Give the keys you want to use with `--field`, as `<path>=<source>` entries separated by `;`. The path is a field (`health_notes`, any collection with such a field) or `database/collection.field`; the source is `key:<base64url>`, `env:<VAR>` or `file:<path>` (prefer the last two: a key on the command line stays in your shell history).

```
backd export --realm clinic --out ./clinic --field 'health_notes=file:./health.key'
backd export --direct --mongo-uri env:MONGO_URI --realm clinic --no-files --out ./clinic-offline \
  --field 'main/patients.tax_id=env:TAX_KEY'
```

- Each key is checked against the **fingerprint** the manifest records. A wrong key is refused by field and key name, before anything is written; so is a `--field` that matches no encrypted field (a typo would otherwise leave the data encrypted unnoticed).
- **Fields without a key stay ciphertext**, and the manifest marks each field `plaintext` or `ciphertext`; the command says which are still encrypted.
- A package with opened fields holds plaintext: keep it, and delete it, as carefully as the keys.

### Without a server, a database or a master key

`backd export-decrypt` opens fields of a package you already have:

```
backd export-decrypt --from ./clinic --out ./clinic-plain \
  --field 'health_notes=file:./health.key;main/patients.tax_id=env:TAX_KEY'
```

It needs the package and the keys, nothing else. A [direct](../direct-mode/) export is the same idea from the database: it needs no running `backd`, and for encrypted fields no master key either, because data keys are wrapped by the field keys. This is also how you read an old `mongodump` backup: restore it into any MongoDB and run a direct export.

## Importing

An import has to put each encrypted field somewhere. The target realm decides, field by field:

| The package holds | The target's field | What happens |
|---|---|---|
| ciphertext | encrypted with **the same key** (same fingerprint) | The target opens the values with its own copy of the key and seals them again under its own data keys. No `--field` needed |
| ciphertext | encrypted with a different key | You give the **source** key with `--field`; the command opens the values and the target encrypts them with its own key |
| plaintext (opened by an export) | encrypted | The target encrypts it, building the blind index |
| plaintext | not encrypted | Imported as it is |
| ciphertext | not encrypted | Refused unless you give the source key |

```
backd import --realm clinic --from ./clinic --dry-run
# these encrypted fields need a key to be imported (--field <path>=<key:|env:|file:…>):
#   main/patients.tax_id: encrypted with the key TAX (fingerprint 9f2c41d0) and the target encrypts it with the key TAX (another one)
backd import --realm clinic --from ./clinic --field 'tax_id=file:./tax.key'
```

The **dry run lists which fields need which key**; a real import refuses (`422`) rather than write ciphertext it can't open. The values are always sealed again under the target's keys, so an import into a realm with another name works too (a value is bound to its realm, and the package records the one it came from). Blind indexes are not in a package: the target builds them as it writes. Importing the same package again finds every document unchanged, though every sealed value is new.

## After losing a realm's secrets

If an instance loses its master key or its secrets, the encrypted data in MongoDB is still there, and so are the wrapped data keys; only the field keys are missing. Save them back **under the same names**:

```
backd secret create --direct --realm clinic --type key --name HEALTH --value file:./health.key
```

`--direct` works on MongoDB and `CONFIG_DIR` with no running `backd`, which matters here: `backd` refuses to start while a field names a key that doesn't exist, so the keys have to come back first.

Its fingerprint must match the one you wrote down (it is printed). Everything becomes readable again, with no export and no import. A field whose key was **never saved** is unrecoverable: that is what field encryption is for, and why [the key page](../../../../build/configuration/field-keys/) asks you to save a key before using it.

{{< hint style="note" title="Where the format is written down" >}}
How values and keys are stored, down to the bytes, with test vectors, is in [Encrypted fields format](../encrypted-format/), so other tools can read the same packages.
{{< /hint >}}
