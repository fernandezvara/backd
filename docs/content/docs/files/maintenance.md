---
title: "Keeping storage tidy"
description: "What happens to a file's object when its document changes or is deleted, erasing users, usage totals, backups, and backd storage usage and reconcile."
icon: "cleaning_services"
weight: 579
toc: true
---

backd never lists the bucket while it runs. Instead, every change that stops a document referencing a file **queues that file's object for deletion**, and the workers (`backd worker`, or `backd serve --with-worker`) delete it, retrying with a growing wait when the storage fails. An upload that never became part of a document is journaled, and the workers delete its object after a grace period (one hour), or when a pending or direct upload expires.

## What queues an object

| Change | Queued |
|---|---|
| A single field's file replaced by an upload | the old file |
| One file removed, or a field cleared | those files |
| A document deleted (`DELETE`, a batch, or a purge from the trash) | every file of the document |
| A user erased, with `on_owner_delete: delete` | every file of the documents they owned |
| A user erased, with `anonymize` and a file field in `remove` | that field's files |
| A user erased, with no `on_owner_delete` | nothing: the files stay with the documents |

A soft-deleted document keeps its files until it is purged. When MongoDB's retention removes it (the `retention` of [`soft_delete`](../../configuration/config-dir/#soft-delete)) nothing is queued, since MongoDB does the removing: [`reconcile`](#reconciling) finds those objects.

## Usage

backd keeps running totals of the bytes and files that documents reference, for the realm and for each user (the owner of the document). A file counts once a document holds it and stops counting when it is queued for deletion. `backd storage usage` shows them:

```sh
backd storage usage --realm acme
```

It prints how the storage is configured (the keys by name, never their values), whether the keys are set and the bucket answers, the realm's totals and the users holding the most, the objects waiting to be deleted (and how many are retrying) and the uploads left unfinished past their time. The same is `GET /v1/{realm}/_admin/storage` in the [admin API](../../auth/admin/).

The totals follow what documents reference. They don't count objects of uploads that never finished, and a document removed by MongoDB's retention isn't subtracted: reconcile frees the objects, not the totals. Enforcing a quota is not built.

## Reconciling

After restoring a database backup, or removing a file field from `collection.yaml`, objects can be left that no document references. Documents restored from a backup may also reference objects that were deleted since: bucket versioning or a lifecycle rule at the provider is the protection for that.

```sh
backd storage reconcile --realm acme            # report only
backd storage reconcile --realm acme --delete   # remove exactly what the report lists
```

It lists the realm's objects (`<prefix>/<realm>/<database>/<collection>/<file id>`, the only time backd lists the bucket) and reports those that no **declared** file field of any document (soft-deleted ones included) references. It always leaves alone:

- objects **younger than 24 hours**;
- objects with an **upload record that isn't `failed`**: uploads in flight, pending uploads not yet expired, and files a document holds, so objects of a restored backup that backd uploaded in the last 90 days are kept even if their document is not there;
- anything that isn't named like a file, and the areas that aren't files (`_check/`, `_logs/`).

Removing a field from `files:` makes its objects unreferenced, and reconcile removes them the same way. The key needs list access on the realm's prefix (see your provider's page), and the [config area with write access](../../auth/admin/); it is audited as `storage.reconcile`.

{{< hint style="warning" >}}
Give each backd instance its **own `prefix`**. Two instances on the same prefix don't know each other's files, and a reconcile on one would delete the other's.
{{< /hint >}}

## Backups

MongoDB backups don't include files, and files aren't in a backup of the database. Back the bucket up at the provider (versioning, replication, or a lifecycle rule that keeps deleted versions for a while), and run `backd storage reconcile` after restoring a database to clean up what the restore left behind.
