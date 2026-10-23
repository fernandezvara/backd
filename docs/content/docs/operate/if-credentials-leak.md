---
title: "If credentials leak"
description: "A runbook for a leaked backup, a credential dump or a stolen key: what to do in the first minutes, in order, what your users will see and how long it takes."
icon: "shield-alert"
weight: 365
toc: true
---

A backup that was not encrypted, a database dump, an API key in a public repository, the secrets master key in a log. This page is the order to work in, with the commands, so that you do not have to design the response while it is happening. Read it once before you need it; the [hardening checklist](../checklist/) points here.

backd already stores what it can safely: passwords as slow [argon2id](../../build/auth/users/#password-storage) hashes, tokens and API keys only as hashes, function secrets encrypted. A leak of that data is serious, not instant: each hash that is guessed opens an account until its user chooses a new password. So the response is to make everything that was in the leak useless, then tell people.

## What leaked, and what to do

| What leaked | What to do |
|---|---|
| A database dump or backup that holds the realm's system data (users, identities, sessions, keys) | Everything below, in order |
| Only session tokens (a log, a proxy cache) | [End every session](#1-stop-the-sessions-and-the-passwords) only |
| An API key | [Revoke it and make a new one](#2-api-keys) |
| The secrets master key (`BACKD_SECRETS_KEY`) | [Rotate it](#3-secrets), then the secrets it protected |
| A [field key](../../build/configuration/field-keys/) (the file you saved), or the data keys of an encrypted field | [Field keys](#5-field-keys) |
| The private key of encrypted backups | [Make a new key pair](#4-backups-and-their-key); old backups stay readable by whoever holds the old key |

## The first minutes

Work in this order. Each step is one command, and none needs the previous one to have finished. The commands need an administrator signed in with `backd login`, or an admin API key in `BACKD_API_KEY` (a session also asks for a fresh proof of identity, because these actions reach every account).

### 1. Stop the sessions and the passwords

```sh
backd user expire-passwords --realm blog --yes --reason "leaked backup" --notify
```

One write sets the realm's **password epoch** to now: every password set before it stops opening its account, and every session ends. Passwords are never changed or deleted, so the audit evidence stays. The answer says how many accounts and sessions it touched. `--notify` also starts the job that emails every user a link to choose a new password (a realm without `email` refuses it, before anything changes: use [reset links](#7-users-the-email-cannot-reach)).

If only sessions leaked, `backd user revoke-sessions --realm blog --all --yes` ends them and leaves passwords alone.

For a planned rotation rather than a breach, add `--keep-sessions` so nobody is signed out.

### 2. API keys

```sh
backd apikey list --realm blog
backd apikey create --realm blog --name billing-2 --expires 90d
backd apikey revoke --realm blog --name billing
```

Revoke every key that could be in the leak and give its users a new one. Keys are only stored as hashes, so a leaked backup does not contain working keys, but a leaked *key* does: revoke it first, then look at the [audit trail](../administration/audit/) for what it did.

### 3. Secrets

If the master key leaked, generate a new one and re-encrypt, as described in [Secrets](../../build/functions/secrets/#storage-and-the-master-key):

```sh
BACKD_SECRETS_KEY=$OLD_KEY BACKD_SECRETS_NEW_KEY=$NEW_KEY backd secret rotate-key
```

Rotating the key protects the stored values from now on; it does not change the values. If the secrets themselves may have been read (a dump with the key), replace each one at its source (the payment provider's key, the OAuth client secrets, the storage keys) and store the new value with `backd secret set`.

### 4. Backups and their key

A leaked backup cannot be recalled: assume whoever has it has everything it held on that day. Make a new `age` key pair, switch the backup job to the new public key and store the new private key apart from the backups ([Backup and restore](../backup/#encrypted-backups)). If a backup leaked *without* its key, the key is not compromised and nothing needs rotating but the data in it.

### 5. Field keys

Encrypted fields have two layers, and what you do depends on which leaked:

- **A copy of a field key** (the file, a laptop, a chat): a dump of the database plus that key reads the field. Replace the key with `backd secret rotate --realm blog --name HEALTH --generate`. It is instant (no document is rewritten), prints the new key once, and re-wraps the data keys. Save the new key; **keep the old copy only for backups made before today**, which need the key that was current then, and treat those backups as exposed to whoever holds the old key.
- **The data keys** (a server's memory, a decrypted dump): the field key alone doesn't cover it. Start a new generation with `backd encrypt rekey --realm blog --database main --collection patients --field health_notes`, then re-seal the stored values with `backd encrypt migrate`. Until the migration ends, old values are still under keys the attacker may have.
- **The secrets master key together with a database dump:** the field keys were among the secrets. [Rotate the master key](#3-secrets), then every field key as above.

Nothing here changes what an attacker already read. Rotation protects values from now on; if the plaintext was exposed, treat it as exposed. See [Field keys](../../build/configuration/field-keys/#change-encryption-on-existing-data) for the table of when to use which.

### 6. Look at what happened

```sh
backd audit --realm blog --since 7d
```

Look for sign-ins from unexpected places, role changes, API keys created, secrets set and exports (`export`, `export.done`). Every action on this page is itself recorded: `password.expire`, `password.expire_notify`, `session.revoke_all`, `password.reset_link`.

### 7. Users the email cannot reach

A user whose mailbox is lost, or a realm without email, gets a one-time link from you instead:

```sh
backd user reset-link --realm blog --email ada@example.com
```

The link is the one a reset email carries: it sets the password, ends the user's sessions and is used up. Hand it over by a channel you trust. For a user you want to lock out meanwhile, `backd user expire-password` or `backd user disable`. See [a reset link without email](../../build/auth/users/#a-reset-link-without-email).

## What your users will see

| Who | What happens |
|---|---|
| A signed-in user | Their session ends; the app asks them to sign in |
| A user signing in with their old password | `401 password_expired` (a wrong password is still `invalid_credentials`, so the answer tells nothing to someone guessing), and the `password-expired` email with a link is queued for them. With `backd-js` this is a `PasswordExpiredError` whose `resetEmailQueued` says so |
| A user who got the email | A page of backd, or of your app if `email.links` points there, to choose a new password. Choosing it signs out their other sessions and sends `password-changed` |
| A user of a provider or a magic link only | Nothing: they have no password to expire |
| A user with a second factor | The same as above: the expired password is refused before the second step starts |

Customize the message with a `password-expired` [email template](../../build/functions/email/); `{{.Data.event}}` carries the reason you gave. The link works for a day at least. A user who lets it lapse uses *Forgot password*, which is always available.

## How long it takes

Measured on a laptop with one local MongoDB and a realm of 5 000 users, each with a session:

| Step | Time | Grows with |
|---|---|---|
| Expire every password and end every session | about 0.05 s | one write and one delete, whatever the number of users |
| Queue the email for every user (`--notify`) | about 19 s (roughly 260 users a second) | the number of users, in a job that renews its lease and can be followed |
| Deliver the emails | your delivery function and provider | worker concurrency (`WORKER_CONCURRENCY`) and the provider's own rate limit |

So the part that protects the accounts is over before you have read the answer; telling 100 000 users takes minutes to queue and as long to deliver as your email provider allows. The [email limits](../../build/functions/email/) apply to each recipient, and a user they hold back is told a little later, not skipped. Follow the job with `backd functions jobs --realm blog` and cancel it with `backd functions cancel`; `backd user notify-expired --realm blog` starts it again without writing to anyone twice.

## After a restore

The epoch lives in the realm's system database. Restoring a backup of that database from *before* the expiry brings the older epoch back, and with it the passwords that were expired. After any restore of older system data, run `backd user expire-passwords` again (see [After a restore](../backup/#after-a-restore)).

## Reference

| What | Where |
|---|---|
| Expire every password | `backd user expire-passwords --realm r --yes [--reason …] [--keep-sessions] [--notify]`, `POST /_admin/passwords/expire`, [the Users page](../administration/admin-ui/) |
| Tell the users, or start again | `backd user notify-expired`, `POST /_admin/passwords/notify` |
| End every session | `backd user revoke-sessions --realm r --all --yes`, `DELETE /_admin/sessions` |
| One user | `backd user expire-password`, `POST /_admin/users/{id}/expire-password`; `backd user reset-link`, `POST /_admin/users/{id}/reset-link` |
| The state | `GET /_admin/passwords` (the epoch and how many accounts have a password) |
| The answer to a user | `401 password_expired`, `PasswordExpiredError` in `backd-js` |
| The email | kind `password-expired` |
| Audit | `password.expire`, `password.expire_notify`, `password.reset_link`, `session.revoke_all` |

The commands and routes in detail are in [Users and passwords](../../build/auth/users/#expiring-every-password) and the [admin API](../administration/admin/).
