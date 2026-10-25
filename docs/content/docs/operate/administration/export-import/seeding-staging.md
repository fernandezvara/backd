---
title: "Seeding staging"
description: "Turn an export into a staging copy that holds no real personal data: erase-policy rules, an override file, a known password, and what stays."
icon: "shield-check"
weight: 20
toc: true
---

A staging or test instance is only as safe as the data in it. This page is about making that data with `backd export --anonymize`; the import is the one of [moving an app](../moving-an-app/#importing).

A staging or test instance is only as safe as the data in it. `--anonymize` turns an export into a copy that holds no real person's details, using what you already declared for [erasing a user](../../erasure/):

```
backd export --realm clinic --include auth --anonymize --anonymize-config staging.yaml \
  --staging-password env:STAGING_PASS --out ./clinic-staging
backd import --realm clinic --from ./clinic-staging     # on the staging instance
```

- **Collections.** Each collection's [`on_owner_delete`](../../erasure/) `remove` and `replace` are applied to **every** document, whoever owns it. They were already checked against the schema, so the documents stay valid. The fields of `pull` and `unset` that hold an **email** are rewritten with the same addresses as the users' (an address that belongs to no user becomes `unknown-<hash>@example.invalid`). A collection without a policy is exported as it is: write one, or use the override file.
- **Users** (with `--include auth`). Emails become `user-<id>@example.invalid`; pending and previous addresses are dropped; identities of external providers (Google, Apple…) stay behind, so nobody can sign in to staging with a real account; password hashes are dropped, or replaced by the hash of `--staging-password`, one password every user can sign in with (as long as the realm's `password.min_length`); second factors and push devices are not exported, and nothing sealed leaves, so no export key is made. Organizations, memberships and teams are kept, like the owners of documents (ids are not personal data). Organizations' sign-in connections are kept with their client ids (and native client ids) blanked, so none of them can sign anyone in on staging.
- **Files** are left out, and the references to them are removed from the documents, unless you pass `--anonymize-files keep`: uploads often hold personal data.
- **The override file** adds rules per collection, on top of the policies, with the same keys and the same checks as an erase policy (the field exists and isn't a system field, a removed field isn't required, a value satisfies the schema, a replaced field isn't in a unique index):

  ```yaml
  collections:
    main/patients:
      remove: [notes, phone]
      replace: {surname: "Doe"}
  ```

  A rule in the file wins over the same field in the policy. A problem is refused before anything is read, naming the collection and field.
- **The manifest is marked `anonymized`.** `import` compares it with the instance's [`BACKD_ENV`](../../../../build/configuration/): a package that is not anonymized into `staging`, or an anonymized one into `production`, draws a warning (never a refusal).

{{< hint style="warning" title="Anonymized is not the same as safe" >}}
Only the fields you name are changed. Free text that holds a name, a field that identifies someone indirectly, or a collection without a policy stays as it is: review the rules for every collection that holds personal data, and look at the copy before you share it.
{{< /hint >}}
