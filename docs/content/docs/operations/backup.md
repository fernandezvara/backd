---
title: "Backup and restore"
description: "What must be backed up, encrypted backups with mongodump and age, restoring, and what to do after a restore."
icon: "backup"
weight: 420
toc: true
---

`backd` keeps all state in MongoDB. The config directory describes the structure (collections, validators, indexes, rules), and provisioning can rebuild it at any time, but not the data. Back up MongoDB, and keep the config directory in version control.

## What a backup must include

| Database | Holds | Rebuilt from config? |
|---|---|---|
| `<realm>__<database>` | your documents | no |
| `<realm>___system` | the realm's users (emails, roles, disabled flag), password hashes, sessions, API keys, invitations, failed-login counters, the [audit trail](../../auth/audit/), encrypted [function secrets](../../functions/secrets/), [invocation history](../../functions/logs/), [async jobs](../../functions/jobs/) (queued, running or recently done) and [idempotency claims](../../functions/calling/#idempotency) (see [system databases](../../operations/#realm-system-databases)) | no, except role seeds |

What the configuration can and can't rebuild:

| Rebuilt from `CONFIG_DIR` by provisioning | Only in a backup |
|---|---|
| Collections, their validators and indexes | Documents |
| The system databases' collections, validators and indexes | Users, their sign-in methods (password hashes) and sessions |
| [Role seeds](../../configuration/realm/#roles) from `realm.yaml`, for users who exist | Role assignments made with `backd user add-role` or the admin API |
| | API keys and invitations |
| | Function secrets' encrypted values, invocation history, async jobs, idempotency claims |

Without a backup of `<realm>___system`, losing MongoDB loses every account, password and API key of the realm; role seeds come back only for users who sign up or are created again. `backd databases` lists every database to back up.

System databases are sensitive:

- Password hashes are argon2id, but weak passwords can still be cracked offline.
- Session tokens, API keys and invitation tokens are stored as SHA-256 hashes of 256-bit random values, so their hashes are useless to an attacker.
- Emails are personal data.

**Encrypt every backup**, and keep the decryption key away from the servers that write the backups.

{{< hint danger >}}
**Back up `BACKD_SECRETS_KEY` separately from database backups, never alongside them.** It's the master key that decrypts every [function secret](../../functions/secrets/) ever stored. Losing it loses every stored secret for good, with no recovery; storing it with a backup defeats that backup's encryption. Keep it in a secret manager, or offline like the [backup key pair](#setting-it-up).
{{< /hint >}}

## Encrypted backups

The [production reference](../production/) includes a backup job, `ops/backup.sh`. It streams `mongodump` straight into [age](https://age-encryption.org/), so no unencrypted copy ever touches the disk:

```sh
mongodump --archive --gzip | age -r age1… > backd-20260927T031500Z.archive.gz.age
```

- **Public-key encryption.** Backups are encrypted to one or more age public keys (`BACKUP_RECIPIENTS`). The server only holds public keys, so a compromised server can't read old backups. List several recipients (for example two operators and a break-glass key kept in a safe) so no single lost key loses the backups.
- **Tamper-evident.** age authenticates what it encrypts: a modified or truncated backup fails to decrypt instead of restoring corrupt data.
- **Least privilege.** The job connects as a `backup` user whose role can only read the collections the config uses (`find`, `listIndexes`, `listCollections`). The provision job creates it when `BACKD_BACKUP_PASSWORD` is set. What that role can read defines what the backup contains: the configured data and system collections, and no MongoDB users or other databases.
- **Never unencrypted.** Without `BACKUP_RECIPIENTS`, the job refuses to run.
- **Retention.** It keeps the newest `BACKUP_KEEP` backups (default 14) in the backup directory and deletes older ones.

### Setting it up

1. On a machine other than the server, create the key pair and keep the private key offline (a password manager, a hardware token, a safe):

   ```sh
   age-keygen -o backup-identity.txt     # prints: Public key: age1…
   ```

   To protect the private key with a passphrase as well, encrypt it: `age -p backup-identity.txt > backup-identity.txt.age`.

2. Put the public key or keys in `.env` on the server, and run the provision job so the `backup` user exists:

   ```sh
   BACKUP_RECIPIENTS=age1… age1…
   ```

   ```sh
   docker compose --env-file .env run --rm --no-deps provision
   ```

3. Schedule the backup, for example nightly from cron:

   ```sh
   15 3 * * *  cd /srv/backd/deploy/production && docker compose --env-file .env --profile ops run --rm --no-deps backup
   ```

4. Copy the backup directory (`BACKUP_DIR`, default `deploy/production/backups/`) off the server, for example with `rclone` or `aws s3 sync` to storage with object lock or versioning, so that neither a server compromise nor ransomware can delete every copy.

5. Alert when no new backup has appeared for more than a day.

The job doesn't need `backd` to be running, and `backd` keeps serving during a backup.

### Consistency

`mongodump` copies collections one after another, not as one snapshot. Writes made during the dump may be only partly included: for example, a user created during the dump might be restored without their password. For a point-in-time snapshot, run MongoDB as a replica set and use your platform's volume snapshots, or a managed MongoDB's continuous backups. Encrypt those too.

## Restoring

Restoring needs the admin credentials and the private key. The restore job, `ops/restore.sh`, decrypts with age and pipes the archive into `mongorestore --drop`. Each collection in the backup replaces the current one; other collections and databases are left alone.

1. Copy the backup file into the backup directory, and stop `backd`:

   ```sh
   docker compose --env-file .env stop backd
   ```

2. Restore:

   ```sh
   docker compose --env-file .env --profile ops run --rm --no-deps \
     -v /path/to/backup-identity.txt:/run/backup-identity:ro \
     restore /backups/backd-20260927T031500Z.archive.gz.age
   ```

   To restore only part of a backup, add `mongorestore` options after the file, for example `--nsInclude='blog__main.*'` for one database.

3. Run the provision job, which reapplies validators, indexes and grants, then start `backd`. It runs in `verify` mode, so it refuses to start if anything is still missing:

   ```sh
   docker compose --env-file .env run --rm --no-deps provision
   docker compose --env-file .env up -d backd
   ```

Remove the private key from the server when you're done.

### Onto a new server

After losing the MongoDB server, restore into a new, empty one:

1. Start MongoDB alone. On an empty volume, its entrypoint creates the admin user from `.env`, and its health check initiates the replica set. Wait until `docker compose ps` shows it healthy (the primary) before restoring:

   ```sh
   docker compose --env-file .env up -d --no-deps mongo
   ```

2. Restore the backup, as in step 2 above.
3. Run the provision job. It recreates `backd`'s least-privilege user and the `backup` user, which a backup of `backd`'s databases doesn't contain, and reapplies validators and indexes.
4. Start the rest: `docker compose --env-file .env up -d`.

Sessions and API keys from the backup work again, so clients don't need new credentials. Then go through [After a restore](#after-a-restore).

### Practise it

Practise restores regularly on a scratch environment. The reference's test (`make prod-test`) runs the whole cycle on every change:

1. a user saves a private draft, and a backup is taken;
2. the whole stack goes down and MongoDB's volume is deleted;
3. a new, empty MongoDB starts, and the test checks it has no `backd` databases;
4. the backup is restored, then the provision job and the stack run;
5. the test checks that the user logs in with their password and reads their own draft (still hidden from anonymous callers), and that an API key and the published documents are back.

## After a restore

{{< hint warning >}}
A restore brings back the state at backup time, **including security state**: keys and users you revoked since, passwords you changed, sessions you ended. Go through this list before you reopen the service.
{{< /hint >}}

Before reopening the service:

- **API keys revoked since the backup work again.** Revoke them again (`backd apikey list`, `backd apikey revoke`). Keys created since the backup are gone; issue new ones.
- **Users disabled or deleted since the backup are back**, with their roles as they were. Disable or delete them again. Roles seeded in `realm.yaml` are reapplied at startup.
- **Password changes since the backup are undone**, so an old password that was changed because it leaked works again. Ask affected users to change their passwords, or set new ones with `backd user set-password`.
- **Sessions come back**, including ones that were logged out. Sessions that have expired since are refused, and MongoDB deletes them. If in doubt, end every session in the realm, and everyone logs in again:

  ```sh
  docker compose --env-file .env run --rm --no-deps provision \
    sh -c 'mongosh "$MONGO_URI" --quiet --eval "db.getSiblingDB(\"blog___system\").sessions.deleteMany({})"'
  ```

- **Failed-login counters** come back too; they expire within 15 minutes.

## If a backup leaks

An encrypted backup is safe for as long as its private key is. If a backup leaks unencrypted, or leaks together with its key, treat it as a breach of the realm's accounts:

- end every session (see above);
- revoke and reissue every API key;
- have every user change their password (`backd` can't send reset emails yet, so plan how to reach them);
- rotate the age key pair.
