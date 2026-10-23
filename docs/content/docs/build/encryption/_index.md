---
title: "Field encryption"
description: "Encrypt some fields before they reach MongoDB, with a key you hold a copy of: what that protects and what it doesn't, how keys work, and where each part is documented."
icon: "lock"
weight: 450
toc: true
---

Some fields (a tax id, a health note, a private comment) must not be readable from a database dump, a backup or an export. **Field encryption** has `backd` encrypt them before they reach MongoDB and decrypt them for the callers your rules allow. The key is yours: `backd` generates it, shows it once, and you keep the only copy outside `backd`.

```yaml
# collection.yaml
fields:
  health_notes:
    encrypt: { key: HEALTH }
    reveal: user != nil && hasRole(user, 'doctor')
```

## What it protects, and what it doesn't

| Protected | Not protected |
|---|---|
| A **database dump** or a **backup** (the values are binary, and open only with the field's key) | A compromised `backd` process: it holds the unwrapped data keys in memory while it runs |
| Anyone with **database access but not the keys**: operators, a stolen disk, a leaked snapshot | Someone who holds the secrets master key **and** the realm's secrets (the keys are among them) |
| An **export** (the server never decrypts for one: ciphertext unless you supply the keys) | Function code that prints a decrypted value to its logs |
| Callers who fail the field's `reveal` rule: they get `null`, and know it is hidden | The **patterns** a blind index shows, if you enable search (equal values have equal indexes) |

It is **not** a replacement for the other layers, and it is not the same as either of them:

- **Disk or database encryption at rest** protects a stolen disk and nothing that can read the running database. Here the database never holds the value in the clear.
- **Bucket encryption** protects files, which this feature does not cover (a file's contents rely on the bucket's encryption).

## How it works, in four lines

1. A **field key** is 32 random bytes that you hold a copy of. It encrypts nothing but other keys.
2. Each field has **data keys** that encrypt its values (AES-256-GCM, each value bound to its realm, collection, field and document, so a value copied elsewhere fails to decrypt). The field key only **wraps** the data keys.
3. That is why **rotating a key is instant**: replacing a field key wraps a handful of data keys again and rewrites no document, whatever the size of the collection.
4. A value is opaque by default. You can opt in to **equality search** with a blind index, and decide **who sees** the plain value with a `reveal` rule.

## Where to read next

| You want to… | Read |
|---|---|
| Create, save, rotate and delete keys; mark fields; search; reveal; change existing data | [Field keys and encrypted fields](../configuration/field-keys/) |
| Copy a recipe: a tax id with lookup, notes for doctors, encrypting a field that has data | [Recipes](recipes/) |
| Understand a startup refusal or an error | [Errors and what to do](errors/) |
| The `collection.yaml` keys | [Data model → Encrypted fields](../configuration/config-dir/#encrypted-fields) |
| Move encrypted data between instances | [Export and import → Encrypted fields](../../operate/administration/export-import/encrypted-fields/) |
| Build a tool that reads the stored format | [Encrypted fields format](../../operate/administration/export-import/encrypted-format/) (with test vectors) |
| Respond to a leaked key | [If credentials leak](../../operate/if-credentials-leak/#5-field-keys) |
| See it work end to end | [Tutorial, chapter 25](../../tutorial/25-private-notes/) |

{{< hint warning >}}
**A key that is lost is a key that is gone.** If you lose a field key and `backd` loses its own sealed copy too, what it protects cannot be recovered. Save a key before you use it, and write its fingerprint next to it.
{{< /hint >}}
