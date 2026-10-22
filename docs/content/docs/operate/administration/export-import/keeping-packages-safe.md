---
title: "Keeping packages safe"
description: "Where to keep an export package and its key, why to avoid raw keys in commands, and when to delete both."
icon: "lock"
weight: 70
toc: true
---

A package holds the data it was made from, and a key can open part of it. These notes are about where to keep them, how to give them to the commands and when to throw them away.

## The package

- **Treat it like the database it came from.** Even with ciphertext and anonymization it holds data. Keep it where only the people who may read the realm can, not in a shared folder, a ticket or a repository.
- **Permissions.** Files `backd` writes are readable only by their owner (`0600`, directories `0700`), and `--archive` makes a `.tar.gz` with the same permissions. Keep them that way when you copy it; copy it over an encrypted channel (`scp`, `rclone` to a private bucket with encryption), not by email.
- **Verify before use.** An import checks every checksum before it trusts anything, so a package changed in transit or at rest is refused. That protects integrity, not secrecy: it doesn't stop someone reading it.
- **Delete it when you are done.** A package is a copy of production that nobody backs up or audits. Remove the directory or archive (and any copy) as soon as the move or report is finished; the audit trail already says what was exported and when.
- **Authentication data is the most sensitive.** With `--include auth` the package holds password hashes, which let anyone with the package guess passwords offline, plus the sealed second factors and push device tokens. Keep that package for as short a time as you can.
- **An extract is still data.** A CSV or a `--fields` package is a lossy view, but it can hold names and addresses: apply the same care, and use `--anonymize` for anything that leaves the team.

## Keys

- **The one-time export key** opens the authenticator secrets, device tokens and Apple tokens of a package made with `--include auth`. It is shown once and `backd` keeps no copy; the manifest holds only its fingerprint. Store it **apart from the package** (a password manager, a secrets store), never inside the same archive or the same ticket, and delete it with the package.
- **Raw keys in commands.** `key:<value>` puts a key in your shell history and in the process list, where other users of the machine can read it. Prefer `env:<VAR>`, which keeps it out of both (set it in the shell, not on the command line), or `file:<path>` with permissions for your user only: a file readable by others draws a warning. The same applies to `--staging-password`, `--mongo-uri` (which holds credentials) and `--master-key`.
- **`--auth-key-out`** writes the key to a file readable only by you instead of printing it, so it never lands in a terminal's scrollback or a CI log. Use it in scripts.
- **The master key** (`BACKD_SECRETS_KEY`) never leaves an instance and is never in a package. A direct export needs it only for what the realm sealed; give it as `env:` or `file:`.
- **In CI**, pass keys as masked secrets through environment variables, never on the command line, and make sure the job doesn't store the package as an artifact.

## What an operator can do

- Through the admin API, only a full administrator can export or import, with a fresh proof of identity for sessions, and every start and end is [audited](../../audit/) with counts, never content. Authentication data is a separate, explicit choice audited as `export.auth`.
- In [direct mode](../direct-mode/) nobody is asked for a credential, because whoever holds MongoDB and `CONFIG_DIR` already holds more than an admin account. The records are still written, as `cli:offline`. Protect that access like the database itself.
- An anonymized export applies only the rules you declared; see [Seeding staging](../seeding-staging/) before sharing a copy.
