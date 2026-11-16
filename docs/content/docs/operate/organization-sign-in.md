---
title: "Organization sign-in for operators"
description: "The realm switch, the limits, the secrets and the checks for letting organizations sign in with their own Google Workspace, Microsoft tenant or OpenID Connect server."
icon: "sign-in"
weight: 355
toc: true
---

Organization sign-in lets the companies that use a B2B app connect their own Google Workspace, Microsoft Entra tenant or OpenID Connect server, so their people sign in with it and join their organization. Its managers set it up; this page is for the person who runs the instance. The guide for the app's developer is [Let a company sign in with its own identity provider](../../build/organizations/sign-in/).

## The switch

It is off unless `realm.yaml` turns it on, and a realm that doesn't answers `404` to the whole surface:

```yaml
organizations:
  sso:
    enabled: true                    # default once the section is there
    providers: [google, microsoft]   # the ones organizations may connect (default: google and microsoft)
    max_per_org: 2                   # connections one organization may have (default and most: the number of providers)
  roles:
    owner: { manage: [org, members, roles, teams, sso] }
```

To let companies bring **their own OpenID Connect server** (Okta, Keycloak, Entra ID, Auth0…) add `oidc` and say which servers are acceptable. It needs [verified domains](../../build/organizations/verified-domains/), because a company's own server can say anything about an address and only a domain the company proved ties the address to it:

```yaml
organizations:
  domains: { enabled: true }
  sso:
    providers: [google, microsoft, oidc]
    oidc:
      issuers:                       # none: organizations cannot use OpenID Connect
        - https://*.okta.com         # one label: https://acme.okta.com, not https://a.b.okta.com
        - https://login.partner.example
  roles:
    owner: { manage: [org, members, roles, teams, sso, domains] }
```

- **An issuer must match a pattern**: an `https` origin, with `*.` for the first label at most, no path, no port other than 443, never a public suffix (`https://*.com` is refused at startup). The check is made again every time the connection is used, so removing a pattern stops its connections at once.
- **backd calls the issuer** (to read its discovery document and keys), so the list is the realm's decision: only the issuer's host and the endpoints it names on the same registered domain are reached, https only, and never a private, loopback, link-local or metadata address (`allow_private_network` is for the providers you write in `realm.yaml`, never for a company's).
- Under `BACKD_DEV=true` a server on this machine (`localhost`, any port, http) is accepted too, to try a local Keycloak.

- `organizations` and authentication must be on; Apple is **not available** to organizations (it gives no proof of which company a person belongs to), and nor are GitHub and plain OAuth 2.
- At least one role needs `manage: sso`, or no member could set a connection up (realm administrators still can, with `backd orgs sso-set` or the admin API). Startup refuses the section otherwise.
- Turning the feature off or removing a provider makes its connections unavailable at once; the stored connections stay.

## What the instance needs

- **`BACKD_SECRETS_KEY`.** The client secrets of the connections are stored sealed with the instance key, in the realm's secret store, at a scope of their own (an organization's). Without the key, setting a connection answers `503`. Nobody can read a client secret back, not the managers and not functions; realm administrators can replace and delete one (`backd secret set|delete --org <id>`) and `backd-server secret rotate-key` re-seals them with the rest.
- **A strategy that starts with `sso`.** A connection signs people in through a [strategy](../../build/auth/#strategies) like any other way in: add `sso: [sso]` to `auth.strategies` (and, for a second step, `sso_totp: [sso, totp]`). With more than one strategy that starts with `sso`, the sign-in names one with `strategy`; without any, a connection signs nobody in (`400 unknown_strategy`).
- **A public address.** The redirect address a company registers with its provider is built from the realm's `email.public_url` or `BACKD_URL`; without either, starting a sign-in answers `503 provider_unavailable`.
- **`backd-server provision`.** The feature adds the system collection `org_connections` and replaces the two unique indexes of `identities` with ones that include the organization; `provision` creates the new ones and drops the old, and with `PROVISION_MODE=verify` instances refuse to start until it has run. Nothing is rewritten.

## Limits and trust

- The roles a connection gives on joining follow the granting rules of an invitation: `manage: sso` alone gives none, `manage: members` the roles that open nothing the manager lacks, `manage: roles` any. backd checks it again at every join: a connection whose last setter could no longer give them is disabled.
- A Microsoft address is accepted only on the domain of the account's user principal name (a tenant's administrator can write any address in the `email` claim, but only verified domains in a UPN), so a tenant can't claim an address of someone else's in your realm. Google's `hd` and verified address do the same job.
- **Accepted risks** (see the review in the design): anyone who can guess an organization's slug can ask which providers it offers (the list is public by design; ids are random, slugs are not, and it is limited to 60 a minute per address); a manager with a tenant can create as many accounts as they have users, even in a `closed` realm (monitor `org.sso.login`, delete the connection to stop it); the blocked list of a connection keeps the newest 1000 removals and only blocks while a connection exists.
- The restriction (Workspace domain or tenant id) is checked on the verified ID token. Shared Microsoft tenants (`common`, `organizations`, `consumers`) are refused when a connection is set.
- Signing in through a connection links to an account by address only on a [verified domain](../../build/organizations/verified-domains/) of the organization (and then tells the owner), and creates users whatever the realm's `signup` mode: only enable the feature if that is what you want for every organization of the realm. The `join_roles` are what a company's own sign-in grants.
- Sign-in routes share the rate limits of the realm's providers (30 starts a minute per address); the list of providers allows 60 a minute.
- Providers are reached through backd's egress-restricted client, at fixed provider hosts: no manager can make backd call an address of their choosing.

## When an organization can't reach its provider

An organization can require its connection (`sso_required`, set by its managers). If its company loses access to the provider, nobody can sign in. A realm administrator clears the requirement: `backd orgs sso-required-off --id <org>` (or `PATCH /_admin/orgs/{org}` with `{"sso_required": false}`), audited as `org.sso_required`. Removing or disabling the last working connection clears it by itself. Administrators cannot turn it on: it needs someone who signed in through the connection.

A session opened through a connection cannot change the person's credentials, link a provider, or delete or export the account (`403 sso_session`); administrators do the last two for people who exist only through a connection.

## Watching it

The [audit trail](../administration/audit/) records `org.sso.set`, `org.sso.deleted` (never the secret), `org.sso.disabled`, `org.sso_required` and one `org.sso.login` for every sign-in with whether it joined the person. `secret.set` and `secret.delete` records carry the `org`. The admin UI's organization page lists the connections and the recent sign-ins.

## Moving and deleting

A deleted organization's connections and secrets go in its cleanup job. `backd export --include auth` carries `org_connections` **without client secrets**: after an import the connections are there but unavailable until a secret is set again (see [Moving users](../administration/export-import/moving-an-app/#moving-users)); an anonymized copy blanks their client ids. If credentials leak, see [If credentials leak](../if-credentials-leak/#3-secrets).
