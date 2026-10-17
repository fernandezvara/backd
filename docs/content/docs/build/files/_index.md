---
title: "Files"
description: "Let documents carry files, kept in the realm's own S3-compatible object storage: how a realm connects its storage, and what backd checks."
icon: "folder-open"
weight: 600
toc: true
---

`backd` itself stores no files. A realm that wants documents to carry files (an avatar, receipts, attachments) **brings its own S3-compatible storage**, from a closed list of providers: [AWS S3](../../operate/providers/aws-s3/), [MinIO](../../operate/providers/minio/), [Cloudflare R2](../../operate/providers/cloudflare-r2/) and [DigitalOcean Spaces](../../operate/providers/digitalocean-spaces/). The bucket stays private: files are only ever reached through `backd`, under the same access rules as the document they belong to.

{{< hint note >}}
**What is available.** Everything below, from [connecting storage](../../operate/providers/storage/) to [files in apps](apps/). The Shelf tutorial chapters that use it are the next step of the Files roadmap.
{{< /hint >}}

Pages: [connecting storage](../../operate/providers/storage/), [file fields](file-fields/), [uploads and downloads](transfers/), [rules for files](rules/), [keeping storage tidy](../../operate/storage-maintenance/), [files in apps](apps/), [security](security/), and [files in functions](functions/).

## The ideas

- **Every file belongs to a document field.** There is no file area outside documents: a public asset or a personal file space is an ordinary collection whose documents carry the file.
- **Storage is a realm's concern.** Each realm declares its own bucket, with its own keys; there is no instance-wide storage.
- **A closed list of providers.** What each provider can do is data in one place in `backd`: a provider that isn't on the list fails the configuration check instead of failing in production, and `backd` never probes a service to find out what it supports.
- **Only what every provider has:** put, get (with ranges), head, delete, listing (for a manual reconcile only) and presigned links, all signed with AWS Signature Version 4.
- **Encryption at rest is the bucket's default,** set at the provider. `backd` sends no encryption headers and has no setting for it; `backd storage check` reports the bucket's status where the provider exposes it.

Start with [Connecting storage](../../operate/providers/storage/) and the page of your provider, then [File fields](file-fields/), [Uploads and downloads](transfers/) and [Rules for files](rules/).
