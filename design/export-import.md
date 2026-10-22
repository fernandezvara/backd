# Design: export and import

- **Issues:** features 17.1–17.8 (milestone `17-export-import`); encrypted fields in 16.6
- **Status:** decided
- **Related designs:** [field-encryption.md](./field-encryption.md) (keys, format, 16.6), [account-lifecycle.md](./account-lifecycle.md) (`on_owner_delete` anonymize policies), [file-storage.md](./file-storage.md), [organizations.md](./organizations.md), [mfa-passwordless.md](./mfa-passwordless.md) (reauth), f6 (job steps)

## 1. The idea

Moving an app between instances, seeding staging from production, handing a report to an analyst, or reading encrypted data from a backup all mean raw `mongodump` today. backd gets one tool for all of it:

- **`backd export`** writes a **package**: a lossless JSON Lines directory with a manifest and files. With `--format csv` it writes a flat view for spreadsheets.
- **`backd import`** reads a package into an instance after validating all of it against the target's config.
- **Through the admin API by default,** or **directly against MongoDB** for offline operations.
- **Encrypted fields** stay ciphertext unless the operator supplies the field's key. Decryption happens only in the CLI, so keys never travel over the network.
- **Authentication data** only when explicitly asked. Secrets never.
- **`--anonymize`** turns a production export into a staging copy, using the erasure policies already declared.

## 2. Decisions at a glance

| Topic | Decision |
|---|---|
| Where | admin API (`GET /_admin/export`, `POST /_admin/import`) by default; `--direct` against MongoDB with `CONFIG_DIR`, for offline use |
| Decryption | only in the CLI, with `--field <path> key:… \| env:… \| file:…`; fields without a key stay ciphertext; the server never decrypts for exports |
| Master key | not needed for data; files need the master key, explicit storage credentials or `--no-files`; secrets never exported |
| Package | `manifest.json` + `<db>/<collection>.jsonl` (API representation, Extended JSON for typed values) + `<db>/_files/` + `_system/`; directory or `.tar.gz` |
| CSV | `--format csv`: flat, one file per collection, export only, documented as lossy |
| Scope | collections, files and wrapped data keys always; authentication data with `--include auth`; never sessions, tokens, counters, jobs, history, secrets or audit |
| Reporting | `--collection`, `--fields`, `--where`; marked `partial`, not importable |
| Import | ids and `_meta` preserved; `create` (default), `skip`, `replace`; full validation before writing; `--dry-run`; resumable; audited |
| Encrypted fields on import | same key: as is; different key: source key supplied, CLI decrypts, target encrypts; plaintext: target encrypts |
| Staging | `--anonymize` applies `on_owner_delete` anonymize settings, rewrites users, keeps encrypted fields as ciphertext; override file |

## 3. Commands

```
# move a whole app, users included
backd export --realm clinic --include auth --out ./clinic-2026-10-09 --archive
backd import ./clinic-2026-10-09.tar.gz --realm clinic --dry-run
backd import ./clinic-2026-10-09.tar.gz --realm clinic

# seed staging
backd export --realm clinic --include auth --anonymize --anonymize-config staging.yaml \
  --staging-password env:STAGING_PASS --out ./clinic-staging

# a report with one decrypted field
backd export --realm clinic --db main --collection patients \
  --fields name,birth_year,tax_id --where 'status = "active"' \
  --field tax_id env:TAX_KEY --format csv --out ./active-patients

# offline, straight from MongoDB (server stopped or unreachable)
backd export --direct --mongo-uri env:MONGO_URI --realm clinic --no-files --out ./clinic-offline

# decrypt fields of an existing package, no server, no database
backd export decrypt ./clinic-2026-10-09 --field health_notes file:./health.key --out ./clinic-plain
```

### 3.1 Supplying field keys

- **Syntax:** `--field <path> <source>`. `<path>` is a field path (`tax_id`, `patient.notes`) or a fully qualified one (`main.patients.tax_id`). A bare path applies to every exported collection that has it.
- **Sources:**
  - `key:<base64url>`: the raw key. The docs warn that it ends up in shell history and process lists.
  - `env:<VAR>`: from an environment variable.
  - `file:<path>`: from a file; permissions wider than owner-only produce a warning.
- **Checking keys:** each supplied key is checked against the fingerprint recorded for that field before anything is written. A wrong key is reported by field and key name.
- **Same syntax everywhere:** export, `export decrypt` and import.

## 4. Where it runs (issues 17.1 and 17.5)

### 4.1 Through the admin API (default)

- **Endpoints:** `GET /_admin/export` streams the package parts, and `POST /_admin/import` receives them. Both need a **full admin with reauth** and are audited (scope, counts, flags, never content).
- **Encrypted fields:** the server sends them as **ciphertext**, together with the wrapped data and index keys. The CLI decrypts fields whose keys were supplied.
- **Imports:** the CLI sends plaintext only for fields the target must encrypt, over the same authenticated connection.
- **Files** are streamed through backd's storage client.
- **Limits:**
  - export streams, so memory doesn't grow with data size;
  - one export or import per realm at a time;
  - long transfers resume from the last completed collection or file.

### 4.2 Direct mode (`--direct`)

- **What it uses:** `CONFIG_DIR` (schemas, `collection.yaml`, encryption settings, indexes) and `--mongo-uri` (or `BACKD_MONGO_URI`). No running server is needed.
- **Master key:** not needed for data, because data keys are wrapped by field keys, not by the master key. It **is** needed for:
  - **files**, since storage credentials are realm secrets. Instead, pass `--storage-key` and `--storage-secret` (as `env:` or `file:`), or use `--no-files`;
  - **authentication data** with realm-encrypted parts (MFA secrets, device tokens), with `--master-key env:…`. Without it, `--include auth` in direct mode exports those parts as unavailable, and the import reports them.
- **Audit:** direct operations write their audit record into the realm's system database, marked `offline`.
- **Safety:** a direct import refuses to run while a backd instance holds the realm's lock, unless `--force`.
- **Old `mongodump` backups:** restore them into any MongoDB and run a direct export. No separate dump decryptor is needed.

## 5. The package (issue 17.1)

```
clinic-2026-10-09/
  manifest.json
  main/patients.jsonl
  main/visits.jsonl
  main/_files/<file id>
  _system/data_keys.jsonl          wrapped data and index keys of encrypted fields
  _system/auth/…                   only with --include auth (§7)
```

**`manifest.json`:**
- the package format version and the backd version;
- realm, databases and collections with counts;
- the source config fingerprint;
- flags (`partial`, `anonymized`, `auth`);
- per encrypted field: key name, fingerprint and whether it was decrypted (`plaintext`) or not (`ciphertext`);
- SHA-256 checksums of every file;
- the creation time and the operator (user id, or `offline`).

**Documents:**
- one per line, in backd's **API representation**: `id`, the fields and `_meta`;
- typed values in MongoDB Extended JSON (relaxed): dates, binary, and ciphertext as binary subtype 6;
- `_expanded` and `_meta.redacted` never appear, and reveal rules don't apply, because exports are administrative.

**Files:** objects stored by file id, with their details already in the documents. Image versions aren't exported; the target regenerates them.

**Integrity:** import verifies every checksum before validating content.

### 5.1 CSV (issue 17.2)

- **Layout:** `--format csv` writes one RFC 4180 file per collection.
  - nested objects are flattened to `a.b` columns;
  - arrays become JSON text in a cell;
  - `_meta` becomes `_meta.owner`, `_meta.org` and the date columns;
  - ciphertext becomes base64 text.
- **Leaves out:** system data and files.
- **Export only:** CSV **can't be imported**, and the docs say it's lossy.

## 6. Scope and reporting (issues 17.1 and 17.2)

| Part | Included |
|---|---|
| collections, files, wrapped data and index keys | always (`--no-files` skips files) |
| users, identities (with password hashes), organizations, memberships, teams, MFA enrollments, push devices | only with `--include auth` at realm scope (§7) |
| sessions, login codes, challenges, email and invitation tokens, counters, jobs, invocation history and logs, push send records | never |
| realm secrets, including `key` secrets | never |
| audit trail | never |

The audit trail stays in `_system` and gets its own `backd audit export` later (§14).

**Scope options:**
- **`--realm r`:** every database of the realm.
- **`--realm r --db d`:** one database, data only.
- **`--collection c`:** one collection (repeatable).

**Reporting extracts:**
- **`--fields a,b.c`:** selects fields (`id` always included).
- **`--where`:** takes the API's filter syntax. Encrypted fields can be filtered only by equality on searchable fields, as in the API.
- **Not importable:** field-selected exports are marked `partial` and refused by import.
- **Encrypted fields** in the selection follow §3.1: plaintext only with their key.

## 7. Authentication data (issue 17.4)

- **When:** only with `--include auth`, at realm scope, by a full admin with reauth. It's audited as a separate event (`export.auth`).
- **What's included:** users, identities including password hashes, organizations, memberships, teams, MFA enrollments and push devices. Still-valid invitations are excluded — they're tokens, like sessions (§6).
- **The export key:** parts encrypted with realm secrets (TOTP secrets, recovery-code hashes keyed by the realm, device tokens, Apple refresh tokens) are decrypted by the server and **re-encrypted with a one-time export key**, generated per export and returned **once** to the operator (fingerprint in the manifest).
- **On import,** the operator supplies the key (`--auth-key env:…`), and the target re-encrypts with its own realm secrets.
- **Without the key,** users are imported without MFA and devices, and the report says so. Required-MFA users then enroll at their next sign-in.

## 8. Import (issue 17.3)

1. **Verify** checksums and the package format version. Partial packages are refused.
2. **Validate everything first** against the **target's** config:
   - schemas, `collection.yaml`, organization settings and indexes;
   - uniqueness, including blind indexes;
   - encrypted fields and their keys (§9);
   - owners and organizations missing from the target.

   The result is a report: counts per collection, conflicts, invalid documents with reasons, missing keys. `--dry-run` stops here.
3. **Write:**
   - **ids and `_meta` are preserved**, so references, expansions, file links and encrypted values (bound to their document id) stay valid;
   - **modes:** `--mode create` (default) fails if any id exists; `--mode skip` keeps existing documents; `--mode replace` overwrites them;
   - **invalid documents** stop the import unless `--skip-invalid`, which writes them to a report file;
   - **files** are uploaded before the documents that reference them;
   - it writes as `backd:import`, as a resumable job with progress steps (f6), and is audited.
4. **Afterwards:** derived data is rebuilt: image versions are queued, and blind indexes are built where values were re-encrypted.

**Config differences:** a different source config fingerprint produces a warning. Validation decides what's accepted.

## 9. Encrypted fields (issue 16.6)

| Package holds | Target field | Import does |
|---|---|---|
| ciphertext | encrypted with the **same** key (fingerprint matches) | imports values and wrapped data and index keys as they are; no key needed |
| ciphertext | encrypted with a **different** key | needs the source key (`--field`); the CLI decrypts and the target encrypts with its own keys and builds blind indexes |
| plaintext | encrypted | the target encrypts and builds blind indexes |
| plaintext | not encrypted | imported as is |
| ciphertext | not encrypted | refused unless the source key is supplied |

The dry run lists which fields need which key, by name and fingerprint. `backd export decrypt <package> --field …` decrypts fields of an existing package with no server or database, using the wrapped keys in `_system/data_keys.jsonl`. The format is specified in [field-encryption.md](./field-encryption.md) §9.

## 10. Anonymized exports (issue 17.6)

- **Collections:** `--anonymize` applies every collection's `on_owner_delete` **anonymize** settings (`remove`, `replace`) to every exported document, whatever the configured `action`. They're already validated against the schemas, so documents stay valid.
- **Users** (with `--include auth`):
  - emails become `user-<id>@example.invalid`;
  - password hashes, MFA enrollments and push devices are dropped;
  - `--staging-password env:VAR` sets one known password for every user;
  - organizations and memberships are kept.
- **Encrypted fields** are always exported as ciphertext under `--anonymize`; `--field` is refused with it.
- **Files** are excluded unless `--anonymize-files keep`, since uploads often hold personal data.
- **Override file:** `--anonymize-config staging.yaml` adds or overrides rules per collection, checked against the schemas like erasure policies.
- **Marking:** the manifest is marked `anonymized`. Importing a package **not** marked anonymized into an instance with `BACKD_ENV=staging` warns, and importing an anonymized one into `BACKD_ENV=production` warns.

## 11. Security considerations

- **Who:** full admins with reauth through the API, and database plus config access in direct mode, audited either way. Authentication data is a separate, explicit, separately audited choice.
- **Keys never on the network:** field keys are used only in the CLI. The server never decrypts encrypted fields for exports.
- **Never exported:** secrets, sessions and tokens. The master key never leaves an instance. Realm-encrypted authentication data travels under a one-time export key.
- **Packages are sensitive:** even with ciphertext and anonymization, they hold data. The docs cover where to keep them, `--archive` permissions, and deleting them after use.
- **Integrity:** checksums verified before import, and full validation before any write.
- **Raw keys in commands:** `key:` sources are discouraged in the docs, with `env:` and `file:` preferred.

## 12. Documentation (issue 17.7)

- **Moving an app between instances:** export, dry run, import, users, keys, files, verifying the result.
- **Seeding staging:** `--anonymize`, the override file, the staging password, what stays ciphertext and why.
- **Reporting extracts:** fields, filters, CSV and its limits, decrypting one field for a report.
- **Encrypted fields:** `--field` sources, the import key table, `export decrypt`, recovery with saved keys.
- **Direct mode:** when to use it, what it needs, what the master key is and isn't needed for, restoring old `mongodump` backups.
- **Reference:** the package format and manifest, CLI flags, admin API, and errors with what to do.

## 13. Issues

| id | Issue | Depends on |
|---|---|---|
| 17.1 | Export packages through the admin API | 13.4 |
| 17.2 | Reporting extracts and CSV | 17.1 |
| 17.3 | Import with full validation | 17.1, f6 |
| 17.4 | Authentication data in export and import | 17.3 |
| 17.5 | Direct mode | 17.3 |
| 17.6 | Anonymized exports | 17.1, 4.6 |
| 17.7 | Export and import documentation | 17.4, 17.5, 17.6, 16.6 |
| 17.8 | Security review of export and import | 17.7 |
| 16.6 | Encrypted fields in export and import (field encryption milestone) | 16.2, 17.3 |

## 14. Alternatives considered

- **CLI straight to MongoDB only, or `mongodump` files only:** rejected for the admin API plus an optional direct mode.
- **JSON Lines only, or CSV importable:** rejected for lossless packages plus an export-only CSV view.
- **Everything except secrets always, or never authentication data:** rejected for explicit `--include auth`.
- **New ids on import, or always replacing:** rejected for preserved ids with safe modes and full validation.
- **Always decrypting and re-encrypting, or ciphertext only:** rejected for automatic paths by key fingerprint.
- **No sanitizing, or a separate masking config:** rejected for `--anonymize` reusing erasure policies.
- **A separate `backd decrypt` over `mongodump`:** replaced by `backd export decrypt` over packages and direct exports.

## 15. Follow-ups

- Scheduled exports to storage (backups in backd's own format) with retention.
- Admin UI: export and import screens with dry-run reports.
- Personal data export for a single user (right of access), reusing the package format.
- `backd audit export`.
