---
title: "Organization sign-in for operators"
description: "The realm switch, the limits, the secrets and the checks for letting organizations sign in with their own Google Workspace or Microsoft tenant."
icon: "sign-in"
weight: 355
toc: true
---

Organization sign-in lets the companies that use a B2B app connect their own Google Workspace or Microsoft Entra tenant, so their people sign in with it and join their organization. Its managers set it up; this page is for the person who runs the instance. The guide for the app's developer is [Let a company sign in with its own Google or Microsoft account](../../build/organizations/sign-in/).

## The switch

It is off unless `realm.yaml` turns it on, and a realm that doesn't answers `404` to the whole surface:

```yaml
organizations:
  sso:
    enabled: true                    # default once the section is there
    providers: [google, microsoft]   # the ones organizations may connect (default: both)
    max_per_org: 2                   # connections one organization may have (default and most: the number of providers)
  roles:
    owner: { manage: [org, members, roles, teams, sso] }
```

- `organizations` and authentication must be on; Apple and generic OpenID Connect are **planned**, not available to organizations yet.
- At least one role needs `manage: sso`, or no member could set a connection up (realm administrators still can, with `backd orgs sso-set` or the admin API). Startup refuses the section otherwise.
- Turning the feature off or removing a provider makes its connections unavailable at once; the stored connections stay.

## What the instance needs

- **`BACKD_SECRETS_KEY`.** The client secrets of the connections are stored sealed with the instance key, in the realm's secret store, at a scope of their own (an organization's). Without the key, setting a connection answers `503`. Nobody can read a client secret back, not the managers and not functions; realm administrators can replace and delete one (`backd secret set|delete --org <id>`) and `backd secret rotate-key` re-seals them with the rest.
- **A public address.** The redirect address a company registers with its provider is built from the realm's `email.public_url` or `BACKD_URL`; without either, starting a sign-in answers `503 provider_unavailable`.
- **`backd provision`.** The feature adds the system collection `org_connections` and replaces the two unique indexes of `identities` with ones that include the organization; `provision` creates the new ones and drops the old, and with `PROVISION_MODE=verify` instances refuse to start until it has run. Nothing is rewritten.

## Limits and trust

- A manager can only give the roles on joining that they could give themselves, and backd checks it again at every join: a connection whose last setter lost the roles is disabled.
- The restriction (Workspace domain or tenant id) is checked on the verified ID token. Shared Microsoft tenants (`common`, `organizations`, `consumers`) are refused when a connection is set.
- Signing in through a connection never links to an account by address and creates users whatever the realm's `signup` mode: only enable the feature if that is what you want for every organization of the realm. The `join_roles` are what a company's own sign-in grants.
- Sign-in routes share the rate limits of the realm's providers (30 starts a minute per address); the list of providers allows 60 a minute.
- Providers are reached through backd's egress-restricted client, at fixed provider hosts: no manager can make backd call an address of their choosing.

## Watching it

The [audit trail](../administration/audit/) records `org.sso.set`, `org.sso.deleted` (never the secret), `org.sso.disabled` and one `org.sso.login` for every sign-in with whether it joined the person. `secret.set` and `secret.delete` records carry the `org`. The admin UI's organization page lists the connections and the recent sign-ins.

## Moving and deleting

A deleted organization's connections and secrets go in its cleanup job. `backd export --include auth` carries `org_connections` **without client secrets**: after an import the connections are there but unavailable until a secret is set again (see [Moving users](../administration/export-import/moving-an-app/#moving-users)); an anonymized copy blanks their client ids. If credentials leak, see [If credentials leak](../if-credentials-leak/#3-secrets).
