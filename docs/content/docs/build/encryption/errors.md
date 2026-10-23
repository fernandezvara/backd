---
title: "Errors and what to do"
description: "Every refusal field encryption can give at startup, in a request or in a command, what it means and what to change."
icon: "alert-triangle"
weight: 20
toc: true
---

Startup refusals name the file, the field and what to do; all problems of a config are reported at once.

## At startup

| You see | It means | Do |
|---|---|---|
| `…: names the key HEALTH, which does not exist` | A field names a key that was never created | `backd secret create --direct --realm r --type key --generate --name HEALTH`, and **save the key**; or `--value` to restore a key you hold |
| `…names HEALTH, which is a plain secret, not a key` | The name belongs to an ordinary secret | Use another name, or delete the secret if nothing uses it |
| `fields are declared encrypted … but the realm has no auth` | Encrypted fields need somewhere to keep keys | Enable `auth` in `realm.yaml` |
| `fields.x: field "x" is not declared in schema.json` | The path isn't a field of the collection | Declare it, or fix the path |
| `…encrypt.key: name the key secret …` | `key` is missing or malformed | Upper-case letters, digits and `_`, starting with a letter |
| `…encrypt.search: "…" is not a kind of search` | Only `equality` exists | Use `search: equality` |
| `…encrypt.normalize: normalization applies to the blind index` / `…"x" is not a normalization` | `normalize` without `search`, or an unknown step | Add `search: equality`; steps are `trim`, `lower`, `upper` |
| `…encrypt.unique: a unique encrypted field needs search: equality` | The blind index is what is unique | Add `search: equality` |
| `system fields (id, _meta) can't be encrypted` | Those are backd's | Encrypt a field of your own |
| `a field inside an array can't be encrypted` | An element has no stable path | Encrypt the array as a whole |
| `a date field (x-backd-store: date) can't be encrypted` | A date is stored as a date, not text | Store it as a string, or leave it plain |
| `x is already encrypted as a whole` | An object and its parts | Encrypt one of them |
| `x is the source of the reference …` | Expanding a reference needs the plain value | Reference a plain field |
| `x is a file field` | File contents rely on bucket encryption | Leave file fields out |
| `x is in an index of indexes.json` / `…in the text index` | An index can't see inside an encrypted value | Use `search: equality` (and `unique`) instead of an index |
| `x is a replace / pull / unset target of on_owner_delete` | Those edit values inside the database | Use `remove` |
| `x is grouped or measured by aggregate` | The database can't compute over encrypted values | Aggregate a plain field |
| `read rules: document.x is encrypted without search: equality` | A read rule is applied inside the database query | Add `search: equality` and compare with `==` or `in`, or move the condition to `reveal` |
| `read rules: document.x is encrypted with a blind index, which only answers == and in` | `!=`, ranges and the rest can't use an index of hashes | Use `==` or `in` |
| `…reveal: unknown field document.y` / `` `data` is only available on create and update `` / `role "z" is not declared` | A `reveal` rule is checked like any rule | Fix the expression; it sees `document`, `user`, `now`, the helpers and `appConfig` |

## In a request

| You see | It means | Do |
|---|---|---|
| `400 field_encrypted` on a `where` or `order_by` | The field is encrypted: only `=`/`$in` on a field with `search: equality` work, and never `null` | Filter on another field, or enable search |
| `500` "an encrypted field can't be read or written: its key is not available" | The key is missing or the instance has no secrets master key | Look at the server log (it names the field, never the value); restore the key with `backd secret create --direct --value …` and check `BACKD_SECRETS_KEY` |
| A value comes back `null` and `_meta.redacted` lists it | Its `reveal` rule said no for this caller | Intended; change the rule or the caller's roles |
| `409` on a write naming the field | A `unique: true` field already holds that value | Choose another value |

## In a command

| You see | It means | Do |
|---|---|---|
| `409` creating a key | A secret with that name exists | Another name; `--skip-existing` in scripts |
| `409` deleting a key: "still used by …" | A field names it, or data keys that encrypted values depend on are wrapped by it | Change the configuration and `backd encrypt migrate` until `status` is complete |
| `409 a rotation of this key is unfinished` | A rotation stopped half way | `backd secret rotate --resume` |
| `409 a migration of this collection is already running` | One job per collection | Wait, or cancel it |
| `the key given for … has the fingerprint …, but … is encrypted with the key …` | Wrong key for an export or import `--field` | Use the key whose fingerprint the manifest records |
| `422 import_refused` naming encrypted fields | Nobody here can open them | Give the source keys with `--field`; the dry run lists them |
