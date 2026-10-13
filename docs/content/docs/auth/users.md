---
title: "Users and passwords"
description: "Create and manage a realm's users with backd user or the admin API."
icon: "group"
weight: 510
toc: true
---

The `backd user` commands manage the users of a realm with `auth: enabled`. They call the realm's [admin API](../admin/) on a running `backd`, so you first [log in](../cli/#logging-in) as an administrator (or set `BACKD_API_KEY` to an admin API key). The realm's first administrator is created with [`backd bootstrap`](../cli/#the-first-administrator). Server-side services can call the admin API directly.

```sh
backd login --realm blog --url https://api.example.com
backd user list --realm blog
```

Every command also takes `--url`, to pick the server for that command.

## Commands

| Command | Effect |
|---|---|
| `backd user create --realm <realm> --email <email>` | Create a user (with a verified address) and ask for their password |
| `backd user create --realm <realm> --email <email> --no-password` | Create a user without a password; they can't sign in with one until it's set |
| `backd user list --realm <realm>` | List the realm's users |
| `backd user set-password --realm <realm> --email <email>` | Set or replace the password, and revoke all of the user's sessions |
| `backd user change-email --realm <realm> --email <email> --new-email <email>` | Change the user's address at once, as an administrator (needs [`email`](../../functions/email/); see [the admin API](../admin/#changing-a-users-email)) |
| `backd user identities --realm <realm> --email <email>` | List the user's ways to sign in: a password and the [providers](../providers/) they use, with the address each reported and when it was linked and last used |
| `backd user unlink-identity --realm <realm> --email <email> --provider <name>` | Remove one of them (`password`, `google`, …); never the last one |
| `backd user owned --realm <realm> --email <email>` | Show what erasing the user would do, per collection that declares a [policy](../../configuration/config-dir/#collectionyaml) |
| `backd user verify-email --realm <realm> --email <email>` | Mark the email as verified |
| `backd user disable --realm <realm> --email <email>` | Block sign-in and revoke all of the user's sessions, without deleting the account |
| `backd user enable --realm <realm> --email <email>` | Let a disabled user sign in again |
| `backd user delete --realm <realm> --email <email> --yes` | **Erase** the user: a tombstone, and the collections' policies applied; waits for the job (`--wait`) and prints the counts. Irreversible: see [Deleting and erasing users](../erasure/) (`disable` keeps the data) |
| `backd user add-role --realm <realm> --email <email> --role <role>` | Assign a role declared in `realm.yaml` |
| `backd user remove-role --realm <realm> --email <email> --role <role>` | Take a role away |
| `backd user networks --realm <realm> --email <email>` | Show the user's [network restrictions](../../configuration/realm/#network-restrictions) |
| `backd user networks --realm <realm> --email <email> --admin <list> --login <list>` | Set them: comma-separated IP addresses or CIDR networks, or `none` to lift one. A list you don't give is kept |

Example:

```sh
backd user create --realm demo --email ada@example.com
# Password:
# Repeat password:
# created user ada@example.com (id d3c9ljp8hc2g00b6s1m0)

backd user list --realm demo
# EMAIL            ID                    VERIFIED  DISABLED  ROLES  CREATED
# ada@example.com  d3c9ljp8hc2g00b6s1m0  false     false            2026-09-26T12:00:00Z
```

On a terminal, passwords are read twice without echo. Otherwise, the first line of standard input is the password, which suits scripts:

```sh
printf '%s\n' "$NEW_PASSWORD" | backd user set-password --realm demo --email ada@example.com
```

{{< hint warning >}}
Deleting a user can't be undone, so the command needs `--yes`. The documents they own are **kept**, with their `owner` pointing at a user that no longer exists; they are not erased.
{{< /hint >}}

## Roles

Roles are declared in [`realm.yaml`](../../configuration/realm/#roles), which can also assign them to users by email. `backd user add-role` and `backd user remove-role` change assignments in the database:

```sh
backd user add-role --realm demo --email ada@example.com --role editor
# role "editor" assigned to ada@example.com
# warning: this assignment is stored only in the database. If the realm is rebuilt from its config, it won't come back.
# To keep it, list ada@example.com under roles.editor.users in demo/realm.yaml.
```

- Only roles declared in `realm.yaml` can be assigned.
- These warnings need the realm's `realm.yaml`, which the CLI reads from `CONFIG_DIR` when it is set (as it is inside the `backd` container). Elsewhere, the CLI prints a general note instead.
- Changes apply from the user's next request. There's no need to sign in again.

{{< hint warning >}}
Roles you assign with the CLI live **only in the database**. If the realm is rebuilt from its config they are gone, and a role that `realm.yaml` assigns comes back at the next `backd serve` or `backd provision` even if you removed it. Put assignments that matter in `realm.yaml`.
{{< /hint >}}
- Removing your own last admin role ends your access to the admin API; see [Command-line administration](../cli/#errors).

## Network restrictions

```sh
backd user networks --realm demo --email ada@example.com --login 10.20.0.0/16
# ada@example.com
#   admin networks: any
#   login networks: 10.20.0.0/16
```

Settings made here live only in the database. If `realm.yaml` sets networks for the same email, they win at the next start, and the CLI warns about it (with `CONFIG_DIR`, as for roles). Admin networks must lie within the realm's `admin.allowed_networks`.

## Emails

- Emails are trimmed and lowercased, so `Ada@Example.com` and `ada@example.com` are the same user.
- Each email belongs to at most one user in a realm.
- Addresses on the reserved `.invalid` domain are refused (erased users' placeholders live there), and so are addresses with a comma, semicolon, colon, angle bracket, parenthesis, square bracket, quote, backslash or control character: a delivery function that joins recipients into one header must never get one address that is really two.
- An address changes only in guarded ways: the user asks with their password and confirms through a link sent to the new address (off unless the realm sets `account.allow_email_change`, see [Sessions](../sessions/#changing-the-email-address)), or an administrator does it through the [admin API](../admin/#changing-a-users-email), which tells both addresses and offers the old one an undo link.

## Password policy

- At least `password.min_length` characters (default 12, set in [`realm.yaml`](../../configuration/realm/)), at most 128.
- Lengths count characters, not bytes, and there are no rules about character classes. Long passphrases are encouraged.
- Passwords are normalized to Unicode NFKC before hashing, so the same text typed on different keyboards or systems matches.

## Password storage

Passwords are hashed with **argon2id** (64 MiB of memory, 3 passes, a random 16-byte salt per password) and stored as standard PHC strings such as `$argon2id$v=19$m=65536,t=3,p=1$…`. The parameters are stored with each hash, so they can be raised later without breaking existing passwords.

Each hash takes about 64 MiB of memory. To keep memory bounded when many passwords are hashed at once, each `backd` process runs at most `PASSWORD_HASH_CONCURRENCY` hashes at a time. By default that is the number of processors Go uses (which follows a container's CPU limit), and never more than fit in half the memory limit (the container's, or `GOMEMLIMIT`): a 4 CPU, 512 MiB container hashes at most 4 passwords at once (256 MiB / 64 MiB), and a 1 CPU container one at a time. The chosen value is logged at startup (`"msg":"password hashing"`). Further requests wait for a free slot; if their deadline passes first, they fail with a "try again later" error instead of exhausting memory.
