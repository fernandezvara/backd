---
title: "Encrypted fields format"
description: "The specification of how backd stores and exports encrypted fields: keys, wrapping, values, associated data, blind indexes and normalization, with test vectors for other tools."
icon: "lock"
weight: 65
toc: true
---

This page is the **specification** of field encryption's storage format, version **1**. It is complete enough to write a tool that reads an export package or a database dump with the field keys alone, with no `backd` and no master key, and the test vectors at the end let that tool prove it. `backd`'s own tests check the implementation against the same vectors, so this page and the code cannot drift apart without a test failing. How to *use* encrypted fields is in [Field keys and encrypted fields](../../../../build/configuration/field-keys/).

## Keys

- A **field key** is 32 random bytes, written in base64url without padding. The developer holds the only copy outside `backd`.
- Its **fingerprint** is the first 8 hexadecimal characters of `SHA-256("backd-field-key-v1|" ‖ key)`. It is for checking that a saved copy is the right one, and is recorded in a package's manifest.
- A **data key** is 32 random bytes with a 16-byte random **id**. One per field and generation. It encrypts that field's values, and nothing else.
- An **index key** is 32 random bytes with a 16-byte id, one per searchable field. It keys the field's blind index and nothing else.

Both kinds are stored only **wrapped** by the field key (below), so replacing a field key re-wraps a few records and rewrites no document.

## Associated data

AES-GCM authenticates, besides the ciphertext, the *associated data* the formats below name. Associated data is built from **length-prefixed strings**: each part is its length in bytes as a 4-byte big-endian integer, followed by the part (UTF-8). Parts are concatenated in order. This keeps two different layouts from ever producing the same bytes.

| What it protects | Parts, in order |
|---|---|
| A value | `backd-field-value-v1`, realm, database, collection, field path, document id |
| A wrapped data key | `backd-field-wrap-v1`, realm, database, collection, field path, kind (`data`), the key's id as 32 lowercase hexadecimal characters |
| A blind index | `backd-field-blind-v1`, field path (this one is the start of the HMAC message, not associated data) |

The *field path* is the dotted path in `collection.yaml` (`address.street`). Because a value is bound to its realm, database, collection, path and document id, a value copied into another document or field fails to decrypt instead of being accepted.

## Wrapping a data key

`wrapped = nonce ‖ AES-256-GCM(field key, nonce, data key, associated data)`, with a random 12-byte nonce and the associated data of a wrapped data key. The result is 12 + 32 + 16 = 60 bytes. An index key is wrapped the same way, with the kind `index` in its associated data.

## A value

The value is serialized as **canonical JSON**: JSON without insignificant white space, object keys sorted by their bytes, `<`, `>` and `&` left as they are, strings escaped only where JSON requires (`"` and `\` with a backslash, `\n`, `\r`, `\t`, `\b` and `\f` by their short forms, other control characters and U+2028 and U+2029 as `\u` followed by four lowercase hexadecimal digits), integers in decimal and other numbers by their shortest round-trip representation; any JSON type, including `null`. Only a blind index depends on these exact bytes, so a tool that only reads values may parse any JSON. It is stored as

| Part | Size | Content |
|---|---|---|
| version | 1 byte | `1` |
| data key id | 16 bytes | the data key that sealed it |
| nonce | 12 bytes | random |
| ciphertext and tag | n + 16 bytes | AES-256-GCM of the JSON under the data key, with the associated data of a value |

In MongoDB it is BSON binary of subtype **6** (encrypted); in an export package it is MongoDB Extended JSON: `{"$binary": {"base64": "…", "subType": "06"}}`. The overhead is 45 bytes per value.

## Blind indexes

A searchable field also stores, in a hidden field of the document, `HMAC-SHA-256(index key, message)` (32 bytes) where the message is the associated-data encoding of `backd-field-blind-v1` and the field path, followed by the canonical JSON of the **normalized** value. Normalization applies the field's `normalize` list in order to a string value (other types are not changed): `trim` removes leading and trailing white space, `lower` and `upper` change case (Unicode simple case mapping). Equal values have equal indexes. A package does not carry blind indexes; they are rebuilt where the package is imported.

## In an export package

- `manifest.json` lists every encrypted field of the package (`database`, `collection`, `path`, `key` (the field key's name), `fingerprint` and `form`). `form` is `ciphertext` when the documents hold the values as above and `plaintext` when they were decrypted with the field key.
- `_system/data_keys.jsonl` holds one JSON object per line for every data and index key of the exported collections' fields: `id`, `database`, `collection`, `path`, `kind` (`data` or `index`), `generation`, `key` (the field key's name), `wrapped` (standard base64), `wrap_version` and `status` (`active`, `decrypt_only` or `retired`).
- To read a value: find the record with the value's data key id, check the field key's fingerprint, unwrap the data key (the associated data of a wrapped key uses the package's `realm` from the manifest), and open the value (the associated data of a value uses the same realm and the document's `id`).

## Versioning

The first byte of a value is the format version. A change to any construction above gets a new version number; `backd` keeps reading the old ones. Package format version and this one are independent.

## Test vectors

Realm `clinic`, database `main`, collection `patients`, field `notes`, document `p1`. Everything is lowercase hexadecimal unless said.

| Step | Value |
|---|---|
| Field key | `000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f` |
| Field key in base64url | `AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8` |
| Field key fingerprint | `e6abcd30` |
| Data key | `202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f` |
| Data key id | `a0a1a2a3a4a5a6a7a8a9aaabacadaeaf` |
| Wrapping nonce | `606162636465666768696a6b` |
| Associated data of the wrapped key | `000000136261636b642d6669656c642d777261702d763100000006636c696e6963000000046d61696e0000000870617469656e7473000000056e6f7465730000000464617461000000206130613161326133613461356136613761386139616161626163616461656166` |
| **Wrapped data key** | `606162636465666768696a6bcafab9769f65a548543fd3005ea0ef6c1644bbbab05104ee9b98ba5740cfc9379635d5703ee0cba0829da2718609df0e` |
| Value nonce | `404142434445464748494a4b` |
| Value (JSON) | `"allergic to nuts"` |
| Associated data of the value | `000000146261636b642d6669656c642d76616c75652d763100000006636c696e6963000000046d61696e0000000870617469656e7473000000056e6f746573000000027031` |
| **Encrypted value** | `01a0a1a2a3a4a5a6a7a8a9aaabacadaeaf404142434445464748494a4be035d1ed3504a26231122447a524240660ac6e0f52147f9b77e4602f2716a38223bc` |

Blind index of the field `tax_id` (index key `808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f`, `normalize: [trim, upper]`), for the value `"  b1234 "`: it normalizes to `"B1234"`, the HMAC message is `000000146261636b642d6669656c642d626c696e642d7631000000067461785f696422423132333422` and the **blind index** is `2bc4883fb24b90833d3495be4aeb6dc7fb3fb9f74ace637ffd32c701af79cc30`.
