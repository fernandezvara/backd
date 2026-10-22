# Design: field-level encryption

- **Issues:** features 16.1–16.9 (milestone `16-field-encryption`)
- **Status:** decided 
- **Related designs:** [mfa-passwordless.md](./mfa-passwordless.md) (reauth for sensitive admin actions), [reference-expansion.md](./reference-expansion.md) (expanded documents), [organizations.md](./organizations.md) (rules used in `reveal`), [file-storage.md](./file-storage.md) (bucket encryption), [export-import.md](./export-import.md) (offline access, 16.6), f6 (job steps)

## 1. The idea

Some fields (tax ids, health notes) must not be readable from a database dump, a backup or a raw export. backd encrypts them before they reach MongoDB and decrypts them for callers who are allowed to read them.

- **A key per field, controlled by the developer:** `collection.yaml` names the key for each encrypted field. A dump can be opened field by field by whoever holds that field's key, without backd's master key and without any other field's key.
- **Envelope encryption:** field keys only wrap **data keys**, and data keys encrypt values. Replacing a field key re-wraps a handful of data keys, so it's instant and rewrites no documents.
- **Keys returned once:** `key` secrets are generated (or imported) through the admin API and returned once. The developer keeps the only copy outside backd.
- **Opaque by default:** encrypted fields can't be queried. Equality search is opt-in through a blind index, with its trade-off explained.
- **Visibility inside the API:** an optional `reveal` rule decides who sees plaintext.
- **Offline access through export:** `backd export --field <path> <source>` decrypts only the fields whose keys are supplied, in the CLI, with a published format. It makes "controlled by the developer" real and doubles as the recovery path.
- **Not covered here:** file contents, which rely on bucket encryption, with encrypted file fields in the backlog.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Key model | envelope: field key (`key` secret) wraps data keys; data keys encrypt values (AES-256-GCM) |
| Cheap updates | replacing a field key re-wraps its data keys only; a new data-key generation plus a migration job is kept for suspected leaks |
| Querying | none by default; `search: equality` adds a blind index (HMAC-SHA-256); ranges, sorting and substrings refused |
| What | any field or nested path of any JSON type, encrypted as a unit; exclusions checked at startup |
| Rules | `create` and `update` read plaintext; `read` and `list` only equality on searchable fields; anything else refused at startup |
| Visibility | optional `reveal` rule per field; `null` plus `_meta.redacted` when hidden; writing doesn't need reveal |
| Key secrets | `type: key`, generated or imported, returned once with a fingerprint, never readable again, full admin plus reauth, audited |
| Existing data | mixed reads; resumable `backd encrypt migrate`; status per field; keys in use can't be deleted |
| Offline | through `backd export` and `backd import` (API or direct): `--field <path> key:\|env:\|file:`, decryption only in the CLI, ciphertext when no key; `backd export decrypt` for packages; published, versioned format with test vectors |
| Files | out of scope; bucket encryption documented; encrypted file fields in the backlog |

## 3. Configuration

### 3.1 Creating keys (issue 16.1)

```
backd secret create --realm clinic --type key --generate HEALTH
  key:          3q2-7wX…(base64url, 32 bytes)   ← shown once, store it safely
  fingerprint:  HEALTH:9f2c41d0
```

- **API:** `POST /_admin/secrets` with `{ name, type: "key", generate: true }`, or `{ name, type: "key", value }` to import an existing 32-byte key. Generated keys are returned **once**, base64url, with the fingerprint.
- **Fingerprint:** the first 8 hex characters of SHA-256 over a fixed label and the key. It lets the developer check that a saved copy is the right one, and export, import and `export decrypt` verify supplied keys with it.
- **Storage:** like every realm secret, encrypted with the master key in `<realm>___system`. **No API, CLI or UI ever returns the value again.** The admin UI shows name, fingerprint, creation and rotation dates, and the fields using the key.
- **Who:** full admins only, with reauth (`X-Backd-Reauth`). Creating, importing, rotating and deleting are audited with name and fingerprint, never the value.
- **Rotating:** `backd secret rotate HEALTH --generate` (or `--value`) returns the new value once and re-wraps every data key and index key wrapped by `HEALTH` (§5.3).
- **Deleting:** refused while any data key, index key or encrypted value still uses the key.

### 3.2 Marking fields (`collection.yaml`)

```yaml
fields:
  tax_id:
    encrypt:
      key: TAX
      search: equality          # optional: blind index for = and uniqueness
      normalize: [trim, upper]  # applied before the blind index only
      unique: true              # unique blind index; requires search
  health_notes:
    encrypt: { key: HEALTH }
    reveal: hasRole("doctor") || doc._meta.owner == user.id
  patient.address:              # nested paths allowed
    encrypt: { key: HEALTH }
```

**Startup checks:**
- the key exists and is of type `key`;
- the path is declared in `schema.json`.

**Startup refuses**, naming the field and explaining why:
- `_meta` fields;
- reference sources and keys (expansion needs plain values);
- paths used in `indexes.json` (use `search: equality` or `unique` instead);
- file fields;
- `replace` targets of `on_owner_delete` (`remove` is allowed);
- `unique` without `search`;
- a path nested inside another encrypted path.

## 4. Stored format (issue 16.2)

### 4.1 Values

An encrypted value is stored as a BSON binary with subtype 6 (encrypted), whose payload is:

| Part | Size | Content |
|---|---|---|
| version | 1 byte | `1` |
| data key id | 16 bytes | identifies the data key (and with it the key name, field and generation) |
| nonce | 12 bytes | random |
| ciphertext + tag | n + 16 bytes | AES-256-GCM over the value serialized as canonical JSON |

- **Associated data:** realm, database, collection, field path and document id. A value copied into another document or field fails to decrypt instead of being accepted.
- **Overhead:** about 45 bytes per value.
- **Validation:** `schema.json` validates the **plaintext** before encryption. backd's generated MongoDB validator declares encrypted paths as `binData`.

### 4.2 Data keys and index keys

`<realm>___system.data_keys`, one record per (database, collection, field, generation):

| Field | Content |
|---|---|
| `id` | 16 random bytes, referenced by values |
| `field` | database, collection, path |
| `generation` | 1, 2, … |
| `key` | the field key's name |
| `wrapped` | the data key encrypted with the field key (AES-256-GCM, with id and field as associated data) |
| `wrap_version` | which version of the field key wrapped it, so an interrupted rotation can resume |
| `status` | `active` (used for writes), `decrypt_only`, `retired` |

- **Index keys:** fields with `search` also get an **index key**, random and wrapped the same way, used for the blind index. It isn't derived from the field key, so replacing the field key never requires rebuilding indexes.
- **Separation:** fields never share data keys, even when they share a field key.
- **In memory:** unwrapped data keys are kept per process and refreshed when keys rotate. Rotation needs no restart.

## 5. Writing, reading and keys (issue 16.2)

### 5.1 Writes

1. The value is validated against `schema.json` as plaintext.
2. `create` and `update` rules see plaintext.
3. backd encrypts with the field's active data key, and for searchable fields computes the blind index.
4. This applies to creates, updates, PATCH merges, `_batch`, functions and system writes alike.

### 5.2 Reads

1. Rules filter, as before.
2. Values are decrypted.
3. `reveal` decides visibility (§7).
4. The blind index is never returned.

Errors never include plaintext or ciphertext. Request logs, audit records and error messages never contain encrypted fields' values or blind-index values.

### 5.3 Rotation

| Situation | Action | Cost |
|---|---|---|
| Routine update of a field key, or a key copy may have leaked | `backd secret rotate NAME` | instant: re-wraps that key's data and index keys |
| Data keys may have leaked (a server's memory, a decrypted dump) | `backd encrypt rekey --field <path>`, then `migrate` | new generation for writes; a job re-encrypts old values |
| Field moved to another key | change `key:` in config, then `migrate` | new data key under the new key; a job re-encrypts |

## 6. Searching and rules (issue 16.3)

### 6.1 Blind index

- **Computing it:** for `search: equality`, backd stores HMAC-SHA-256 of the normalized plaintext, keyed by the field's index key, in a hidden field (`_bidx.<path>`), indexed. With `unique: true` the index is unique (`409 duplicate` as for other unique fields).
- **Querying:** `where tax_id = "B1234"` is translated to a comparison on `_bidx.tax_id`, and `in` with a list of values works the same way.
- **Refused:** ranges, sorting, `$contains`, prefixes and comparisons between fields answer `400 field_encrypted`, naming the field.
- **Trade-off, in the docs:** equal values have equal index values, so a dump shows which documents share a value, though not the value itself. Fine for high-variety values (tax ids, emails, card fingerprints). Not recommended for low-variety ones (blood type, yes/no, country).

### 6.2 Rules

- **`create` and `update`:** can read encrypted fields, evaluated in memory on plaintext.
- **`read` and `list`:** can use an encrypted field **only** for equality on a searchable field, pushed down through the blind index (`doc.tax_id == user.tax_id` is valid when `user.tax_id` is a value).
- **Anything else** in a `read` or `list` rule is **refused at startup**, with the rule file, the rule and the field. A rule can never silently behave differently.
- **Property tests:** the pushdown property tests gain encrypted fields, both searchable and not.

## 7. Who sees plaintext (issue 16.4)

- **`reveal`:** an optional expr rule per encrypted field, evaluated in memory per document after the read rule, with the usual `user`, `doc`, `hasRole` and organization helpers.
  - **Hidden fields:** the field comes back as `null`, and the document lists it in `_meta.redacted`, so apps can tell "hidden" from "empty".
  - **No `reveal`:** whoever can read the document sees the plaintext.
- **Writing doesn't need reveal:** a caller allowed to update can set or replace the field without being able to read it. PATCH responses apply `reveal` too.
- **Functions:** `ctx.db` follows the caller's `reveal`. `ctx.admin.db` sees plaintext.
- **Admin API:**
  - full admins see plaintext, and each admin data read that reveals encrypted fields produces one audit record (fields and count, never values);
  - read-only admins always get redacted fields.
- **Expansion:** expanded documents apply their own collection's `reveal`.
- **Conditional GET:** responses with redacted fields carry the representation tag in their `ETag` (`"v12.r3f9a"`, see [conditional-get.md](./conditional-get.md)), so a caller whose `reveal` outcome changes never gets a stale `304`. `If-Match` still compares the version only.
- **JS client:** `_meta.redacted` is typed, and `field_encrypted` is a typed error.

## 8. Changing encryption on existing data (issue 16.5)

- **Reads accept every form:** plaintext left from before, values under older generations, and values under a previous key that still exists.
- **Writes always follow the current config.**
- **`backd encrypt migrate --realm --db --collection [--field] [--dry-run]`** runs a resumable, rate-limited job with progress as job steps (f6). In one pass it:
  - encrypts plaintext values;
  - re-encrypts values under old generations or keys;
  - builds or rebuilds blind indexes;
  - decrypts values when `encrypt` was removed from the field.

  When all values of a generation are migrated, its data key becomes `retired`.
- **Status:** `GET /_admin/encryption` and `backd encrypt status` give, per field, counts of plaintext, current and old values, the key and fingerprint, generations and blind-index coverage. Startup logs fields that aren't fully migrated.
- **Searches while migrating:** equality searches can miss documents whose blind index isn't built yet. The docs say so, and the status shows coverage.
- **Retiring a key:** a key can't be deleted while any value or data key uses it, and the status shows what still does.

## 9. Encrypted fields in export and import, and recovery (issue 16.6)

Offline access to encrypted data is part of export and import ([export-import.md](./export-import.md)). There is no separate dump decryptor.

```
# a package with one field decrypted; every other encrypted field stays ciphertext
backd export --realm clinic --field health_notes file:./health.key --out ./clinic

# offline, straight from MongoDB, no server and no master key
backd export --direct --mongo-uri env:MONGO_URI --realm clinic --no-files \
  --field tax_id env:TAX_KEY --out ./clinic-offline

# decrypt fields of an existing package, no server and no database
backd export decrypt ./clinic --field tax_id key:3q2-7wX... --out ./clinic-plain
```

- **Supplying keys:** `--field <path> <source>`, with `key:<base64url>`, `env:<VAR>` or `file:<path>`.
  - Each key is checked against the fingerprint recorded for the field, and a wrong key is reported by field and key name.
  - **Fields without a supplied key stay ciphertext** in the output, and the manifest marks each encrypted field as `plaintext` or `ciphertext`.
- **Decryption only in the CLI:** the server always sends ciphertext with the wrapped data and index keys. Field keys never travel over the network, and the server never decrypts for an export.
- **No master key needed:** data keys are wrapped by field keys, so a direct export (§4.2 of the export design) decrypts with field keys alone.
- **Import:**

  | Package holds | Target field | Import does |
  |---|---|---|
  | ciphertext | same key (fingerprint matches) | imports values and wrapped keys as they are; no key needed |
  | ciphertext | different key | needs the source key; the CLI decrypts and the target re-encrypts and builds blind indexes |
  | plaintext | encrypted | the target encrypts and builds blind indexes |
  | plaintext | not encrypted | imported as is |
  | ciphertext | not encrypted | refused unless the source key is supplied |

  The dry run lists which fields need which key.
- **Anonymized exports** always keep encrypted fields as ciphertext.
- **Old `mongodump` backups:** restore them into any MongoDB and run a direct export.
- **Published format:** values, data-key and index-key wrapping, associated data, normalization and the blind index are documented in a versioned spec with **test vectors**, so other tools can implement it.
- **Recovery:** if an instance loses its master key or its realm secrets, importing the saved keys under the same names (`backd secret create --type key --value`) makes all data readable again. A field whose key was never saved is unrecoverable, and the docs say so at every place a key is created.

## 10. Shelf (issue 16.7)

- **Private notes:** a Shelf item gains `private_notes`, encrypted with a `SHELF_NOTES` key, with `reveal: doc._meta.owner == user.id || hasOrgRole(doc._meta.org, "admin")`.
- **What the chapter shows:**
  - creating the key and saving it;
  - a member seeing `null` with `_meta.redacted`, and the team admin seeing the notes;
  - the raw document in MongoDB showing ciphertext;
  - `backd export --field private_notes file:…` producing a package where only the notes are readable, and the same export without the key showing ciphertext;
  - rotating the key instantly.
- **Tests:** Shelf code and tests are updated, and earlier chapters still pass.

## 11. Security considerations

- **What it protects:** dumps, backups, exports, database operators and anyone with database access but without the field keys, and API callers who fail `reveal`.
- **What it doesn't protect:** a compromised backd process (it holds unwrapped data keys in memory), anyone holding the master key **and** the realm's secrets, function code that prints decrypted values to logs, and patterns revealed by blind indexes.
- **Integrity:** AES-GCM with associated data stops tampering and stops values being moved between documents or fields.
- **Key handling:** values returned once and never readable again, fingerprints for checking, full admin plus reauth, audit, no deletion while in use.
- **Logs:** no plaintext or blind-index values in backd's logs or errors.
- **Outside review:** the format and the blind index get an outside cryptography review before production use (16.9).

## 12. Documentation (issue 16.8)

- **Concepts:**
  - what field encryption protects and what it doesn't (§11), in plain words;
  - field keys, data keys and why rotation is instant;
  - the difference from bucket encryption and from disk encryption.
- **Key handling guide:**
  - creating and saving keys (with a warning that a lost key means lost data);
  - fingerprints;
  - importing for restores and staging;
  - rotation, rekeying and migration, with a table of when to use which.
- **Search trade-offs:** when to opt into equality search, normalization, uniqueness, and the low-variety warning.
- **Rules and `reveal`:** what rules can use, the startup errors, `reveal` recipes (owner, doctor role, organization admin).
- **Recipes:** encrypting a tax id with lookup, health notes visible to doctors, turning encryption on for an existing field.
- **Offline:** encrypted fields in export and import (pointing to the export guide), `export decrypt`, and the format spec with test vectors.
- **References:** `collection.yaml` and the admin API, the CLI and OpenAPI.

## 13. Issues

| id | Issue | Depends on |
|---|---|---|
| 16.1 | `key` secrets | 13.4 |
| 16.2 | Encrypted fields | 16.1 |
| 16.3 | Equality search, uniqueness and rules on encrypted fields | 16.2 |
| 16.4 | Reveal rules | 16.2 |
| 16.5 | Migrating existing data | 16.3, f6 |
| 16.6 | Encrypted fields in export and import, format spec | 16.2, 17.3 |
| 16.7 | Shelf private notes | 16.4, 14.10 |
| 16.8 | Field encryption documentation | 16.5, 16.6, 16.7 |
| 16.9 | Security review with outside cryptography review | 16.8 |

## 14. Alternatives considered

- **Field keys encrypting values directly, or one realm key:** rejected for envelope encryption with keys per field.
- **Never queryable, or MongoDB Queryable Encryption:** rejected for an opt-in blind index; Queryable Encryption needs cgo, Enterprise or Atlas, and doesn't work on DocumentDB.
- **Top-level strings only, or whole documents:** rejected for any path and type.
- **Rules never using encrypted fields, or in-memory list filtering:** rejected for correct-or-refused rules.
- **Encryption at rest only, or a general field permission system:** rejected for `reveal` on encrypted fields.
- **Keys exportable later, or generated only:** rejected for return-once plus import.
- **Restoring into backd only, or a spec without a tool:** rejected for decryption in export plus a spec.
- **A separate `backd decrypt` over `mongodump`:** replaced by `--field` in `backd export` (including direct mode) and `backd export decrypt` for packages, so there's one tool for moving and reading data.
- **Refusing to start on mixed data, or lazy re-encryption:** rejected for mixed reads plus a migration job.
- **Encrypted file contents now:** deferred.
- **Index keys derived from field keys:** replaced by random index keys wrapped like data keys, so rotation never rebuilds indexes.

## 15. Follow-ups

- Encrypted file fields: per-field key, proxy-only transfers, workers decrypting for image versions, file objects in export with `--field`.
- Per-user keys for crypto-shredding (erasing a user by destroying their key).
- Admin UI: encryption status, keys with fingerprints and usage, migration jobs.
- External key management (cloud KMS or Vault) to hold field keys outside backd.
