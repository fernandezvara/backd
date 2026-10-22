---
title: "Moving an app between instances"
description: "Export a realm with its files and users, check the import with a dry run, import it, and verify the result."
icon: "folder-open"
weight: 10
toc: true
---

This is the whole journey of moving an app, users included, from an instance you call *old* to one you call *new*.

1. **Prepare the new instance.** Its `CONFIG_DIR` needs the same databases and collections as the old one (the schemas may differ: the import tells you what the new ones refuse). Provision it (`backd provision`, or start it) and make sure the realm has authentication enabled and, if documents hold files, a `storage:` section whose secrets are set.
2. **Export from the old instance**, keeping the one-time key apart from the package:

   ```
   backd export --realm clinic --include auth --out ./clinic-2026-10-09 --archive --auth-key-out ./clinic.key
   ```

3. **Check what would happen on the new one**, with nothing written:

   ```
   backd import --realm clinic --from ./clinic-2026-10-09.tar.gz --auth-key file:./clinic.key --dry-run
   ```

   Read the report: counts per outcome, and the first documents that are invalid or conflict, with their line and reasons. Fix the configuration or the data and run the dry run again until it is clean. Warnings (a different configuration, owners the target lacks) are worth reading but don't stop an import.
4. **Import**, with the same command without `--dry-run`. Files are uploaded first, then the documents are written with their ids and `_meta` kept.
5. **Check the result.** The command prints how many documents it created, replaced, left unchanged or skipped. Sign in as a user and open a document with a file; run `backd export` on the new instance and compare the counts in both manifests.
6. **Clean up.** Delete the package and the key (see [Keeping packages safe](../keeping-packages-safe/)), and stop the old instance when you are ready.

## Exporting

```
backd export --realm clinic --out ./clinic-2026-10-09
backd export --realm clinic --out ./clinic-2026-10-09 --archive     # ./clinic-2026-10-09.tar.gz
backd export --realm clinic --db main --collection patients,visits --out ./clinic-main --no-files
```

| Flag | Effect |
|---|---|
| `--out` | The package directory to write (required). With `--archive` it ends as `<out>.tar.gz`, readable only by you, and the directory is removed |
| `--db` | Only this database of the realm |
| `--collection` | Only these collections of `--db`, comma-separated |
| `--no-files` | The documents only: leave the files out |
| `--fields` | A [reporting extract](../reporting-extracts/): only these fields |
| `--where` | Only the documents matching a filter, for one `--collection` |
| `--format` | `jsonl` (a package, the default) or `csv` |
| `--anonymize` | Make a [staging copy](../seeding-staging/) |
| `--include auth` | Also export the realm's [authentication data](#moving-users) |
| `--auth-key-out` | With `--include auth`: write the export key to this file instead of showing it |

It needs **full admin access** and, when you are signed in with a session, a fresh proof that it is you (the same one [creating an API key](../../admin/) asks for). With an admin API key in `BACKD_API_KEY` nothing more is asked. Both start and end of every export are [audited](../../audit/) (`export`, `export.done`) with the scope and the counts, never the content.


## Importing

```
backd import --realm clinic --from ./clinic-2026-10-09 --dry-run
backd import --realm clinic --from ./clinic-2026-10-09
backd import --realm clinic --from ./clinic-2026-10-09.tar.gz --mode replace
```

`--from` is a package directory or the `.tar.gz` that `--archive` wrote. Like the export, it needs full admin access and a fresh proof of identity for sessions, and it is [audited](../../audit/) (`import`, `import.done`). The import goes in this order, and **nothing is written until all of it is known**:

1. **The package is verified.** The manifest's format must be one this `backd` reads, and the SHA-256 of every file it lists must match. A package made with `--fields` (partial) and a CSV extract are refused.
2. **Everything is validated against the target realm's own configuration**, which may differ from the source's: each collection must exist there, and every document is checked as the API would check one a client sent: its id (the collection's `custom_id` rules, or a generated id), its fields against `schema.json`, its files against the file fields (and their size limits), and its unique indexes against what the target holds. The result is a report: counts per outcome, and the first invalid or conflicting documents with their line and reasons. **`--dry-run` stops here.**
3. **The files are uploaded**, through the target's storage, before the documents that reference them.
4. **The documents are written** with their **ids and `_meta` kept** (creation and update times, version, owner, trash state), so references between documents, file links and ownership stay valid. Files are counted in the realm's storage usage and the image versions their fields declare are queued for the workers: versions are not in the package.

| `--mode` | A document whose id exists in the target |
|---|---|
| `create` (the default) | The same document is left alone; a **different** one is a conflict and nothing is written |
| `skip` | The existing document is kept |
| `replace` | The existing document is overwritten, and files it no longer holds are queued for deletion |

Invalid documents stop the import. `--skip-invalid` leaves them out instead and writes them, with their reasons, to `--report` (`import-report.json` by default). A conflict always stops it.

**An interrupted import is finished by running the same command again**: a document already written is found identical and left alone, whatever the mode, and files are uploaded again with the same bytes. Documents are sent in chunks, so memory doesn't grow with the package.

{{< hint style="warning" title="The import writes like any other writer" >}}
Triggers and realtime subscriptions see imported documents as ordinary creations and updates. Documents of users and organizations the target doesn't have are imported and counted in the report: their owners can't sign in there until you bring the users across. The config fingerprint of the package is compared with the instance's and a difference is a warning, never a refusal: validation decides what is accepted.
{{< /hint >}}


## Moving users

A package holds documents only: the users who own them stay behind unless you ask. `--include auth` adds, for the **whole realm** (not with `--db`, `--collection`, `--fields` or CSV), `_system/auth/<collection>.jsonl` for:

| Collection | What it holds |
|---|---|
| `users`, `identities` | Accounts and their sign-in methods, **password hashes included** |
| `orgs`, `org_members`, `org_teams` | Organizations, memberships (with their roles) and teams |
| `mfa` | Second-factor enrollments (authenticator apps) |
| `devices` | Push notification devices |

Never included: sessions, login codes, challenges, email and invitation tokens, API keys, secrets and the audit trail. Users are signed out of the old instance's sessions, not carried: they sign in again.

Some of those fields are sealed with the **realm's secrets key** on the instance they come from: the authenticator secrets, the device tokens and Apple refresh tokens. That key never leaves an instance, so the server opens them and seals them again under a **one-time export key** it makes for this export and shows to you **once**:

```
backd export --realm clinic --include auth --out ./clinic-auth --auth-key-out ./clinic.key
backd import --realm clinic2 --from ./clinic-auth --auth-key file:./clinic.key
```

- Without `--auth-key-out` the key is printed on the terminal, once; `backd` keeps no copy. The manifest records only its fingerprint, which `import` checks the key against.
- On import, `--auth-key` takes `key:<value>`, `env:<VAR>` or `file:<path>` (a file readable by others draws a warning; the value form ends up in shell history and process lists, so prefer the others). The key is used **only on the machine running the import**: it opens the sealed fields there and sends them over the authenticated connection to be sealed under the target realm's own key.
- **Without the key**, users, identities, organizations, memberships and teams are imported, but without second factors, push devices and Apple tokens. The command says so; users with required second factors enroll again at their next sign-in.
- **Recovery codes don't travel.** They are keyed hashes under the realm's master key and can't be re-keyed: users with an authenticator keep it and generate new recovery codes.
- Authentication data is fetched whole on every run, even when resuming, because the export key belongs to one run. Import it first: it is sent before the documents, so the owners they name exist when they are checked; documents owned by users that are in the package are not reported as missing.
- Importing uses the same modes as documents (`create`, `skip`, `replace`) and the same idempotence. The store checks each document as it is — the id, unique values such as emails and provider identities, the shape its collection keeps — and a repeated email is a conflict.

{{< hint style="warning" title="The package then holds credentials" >}}
Password hashes let anyone with the package try passwords offline, and the export key opens the authenticator secrets. Keep the package and the key apart, in different places, and delete both when the move is done. Both actions are audited (`export.auth`, `import`).
{{< /hint >}}


## Big realms and interruptions

The server reads each collection in pages and streams it, so memory does not grow with the size of the data. Only **one export of a realm runs at a time** (`409 export_running`); an export that goes silent for ten minutes loses its place.

Every part is checked as it arrives and listed in `<out>/.export-progress.json`. If the command stops, **run the same command again**: parts already complete are kept (they are checked for their size) and the export continues from the next. A part that was in flight starts again from its beginning.

{{< hint style="warning" title="A package holds your data" >}}
Treat a package like the database it came from: keep it where only the people who may read the realm can, do not commit it, and delete it when you are done. It is written with owner-only permissions.
{{< /hint >}}


## Not a backup

Collections are read one after another while the app keeps working, so a package is not a snapshot of one instant, and it leaves out the system data a restore needs. For backups see [Backup and restore](../../../backup/).
