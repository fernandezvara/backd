---
title: "Let a company sign in with its own Google or Microsoft account"
description: "A guide for B2B apps: an organization connects its Google Workspace or Microsoft Entra tenant, its people sign in with it and join automatically, and nobody outside the company gets in."
icon: "sign-in"
weight: 529
toc: true
---

A company that uses your app usually has its own identity provider already: a Google Workspace or a Microsoft Entra tenant. With **organization sign-in** its people click "Continue with Google" or "Continue with Microsoft", land in **its** organization with the roles you chose, and nobody from another company can come in, without you editing files or handling the company's secrets. The company's managers set it up themselves.

This page is the guide, in the order you do it. The routes and the rules behind it are in [the organizations API](../api/#sign-in-connections); the realm's switch and limits are in [Organization sign-in for operators](../../../operate/organization-sign-in/).

## What you get

- **A connection per provider.** An organization has at most one Google and one Microsoft connection: the application the company registered with the provider, a client secret, and who may come in.
- **The restriction is cryptographic.** A Google connection names the Workspace domain and backd checks the `hd` of the verified ID token; a Microsoft connection names the company's tenant id and backd checks the token's `tid` and issuer. Not a list of email domains a manager could claim: a person from another company, or with a personal account, is refused and nothing is created.
- **People join by signing in.** The first time someone the connection admits signs in, they get an account and become a member with the connection's `join_roles`, even when the realm's `signup` is `closed`.
- **No account takeover.** Signing in through a connection never links to an existing account by email address. Someone who already has an account links the connection explicitly.

## 1. Switch it on

An operator turns the feature on in `realm.yaml`, and gives some organization role the capability to manage it:

```yaml
organizations:
  sso: { providers: [google, microsoft] }
  roles:
    owner: { manage: [org, members, roles, teams, sso] }
    admin: { manage: [members, teams, sso] }
    member: {}
```

The instance also needs `BACKD_SECRETS_KEY` (the client secrets are stored sealed) and a public address for the redirect (`BACKD_URL`, or the realm's `email.public_url`). See [Organization sign-in for operators](../../../operate/organization-sign-in/).

## 2. Register the application with the provider

The company's administrator creates an OAuth application in **Google Cloud** (an *OAuth client ID* of type *Web application*) or in **Microsoft Entra** (an *App registration* in the company's own tenant; add the optional `email` claim to its ID token, since backd needs the address), and gives it the **redirect address** backd shows when the connection is made:

```
https://api.acme.example/v1/shop/_auth/orgs/<organization id>/oauth/google/callback
```

It names the organization by its **id** (not its slug), so it never changes when the organization is renamed. The same page of the admin UI (the organization's *Sign-in* section), `GET /_orgs/{org}/sso` and `backd orgs sso` print it. Keep the application's **client id** and **client secret**.

## 3. Set the connection

The manager, signed in to your app, sets it. It needs `manage: sso` and a fresh proof of identity:

```js
const sso = backd.orgs.sso(org.id)
await backd.auth.withProof(() =>
  sso.set('google', {
    clientId: '123-abc.apps.googleusercontent.com',
    clientSecret,                   // write-only: it is sealed and never returned
    hostedDomain: 'acme.example',   // Microsoft: tenant: '<the tenant id>'
    joinRoles: ['member'],          // given to people who join through it
  }))
```

- **The restriction is required.** Google needs the Workspace `hostedDomain` (a personal Google account has none and is refused); Microsoft needs the company's `tenant` id, a GUID: `common`, `organizations` and `consumers` admit people of any company and are refused.
- **`joinRoles` follow the granting rules.** A manager can't give a role that opens capabilities they don't hold themselves, the same rule as an invitation's. A manager who holds `manage: sso` and `manage: members` can give roles that open nothing they lack (like `member`), one with `manage: roles` any role, and one with only `manage: sso` none (the roles of a connection go to every person of the company). Leave `joinRoles` empty and people get the realm's `default_roles` only.
- **`emailDomains` narrows further** (the verified address must also be in one of those domains) and **`nativeClientIds`** adds the client ids of your mobile apps.
- Realm administrators can do the same for any organization with `backd orgs sso-set` and the admin API.

Without a client secret stored the connection is *unavailable*: the admin UI says so.

## 4. Offer the button

Ask which providers to show for an organization instead of hard-coding them. The list holds only connections that work right now, and it is empty for an unknown organization, so it does not say who your customers are:

```js
const names = await backd.auth.providers({ org: 'acme-corp' })   // ['google']
// ... a "Continue with Google" button for each name
await backd.auth.signInWith('google', { org: 'acme-corp' })
```

`org` is the organization's id or slug (your app knows it from the company's sign-in page, a subdomain or a field the user types). The rest is the [provider flow](../../auth/providers/): the client finishes the sign-in when the page loads, and a mobile app sends the platform's ID token to `signInWithIdToken(provider, { idToken, nonce, org })`.

## 5. Test it

1. Sign in with a test person of the company: they become a member with the `joinRoles` (see `GET /_orgs/{org}/members`, or the organization's page in the admin UI, which also lists the recent sign-ins).
2. Sign in with an account from another domain or a personal one: the page gets `backd_error=not_allowed` and the client throws a **`NotAllowedError`**; no user was created.
3. In a realm with `signup: closed` the company's people still come in: the connection's restriction is the gate.

## What happens to existing accounts

| The person | Result |
|---|---|
| Has no account | Made, signed in, joined with `joinRoles` |
| Has an account with that address | `backd_error=account_exists`. They sign in the way they already do, then `backd.auth.linkProvider('google', { org })`: that links the connection and joins them |
| Signs in again later | The same user; nothing changes, not even their roles |
| Was removed from the organization by a manager | Signed in but **not joined**, until they accept an invitation to the organization. Leaving by oneself does not block |

Addresses that come through a connection are stored **unverified**, so your own email verification still applies. For Microsoft, backd believes the address only when it is on the same domain as the account's user principal name (`preferred_username`), because a tenant's administrator can write any address in the `email` claim but can only give accounts the domains the tenant has verified: that is what stops a tenant from claiming someone else's mailbox in your realm. Guests whose address is on another domain are refused until verified domains arrive. A person can hold an identity with the realm's Google and another with the organization's, and one per provider per organization; `DELETE /_auth/identities/{provider}?org=` removes the one of an organization (never the last way to sign in).

## When it stops working

- **The manager who set the connection loses a role.** If `joinRoles` gave capabilities and the manager who last set the connection no longer holds them, backd **disables the connection** the next time someone signs in (`org.sso.disabled` in the audit trail). Set it again with a manager who holds the roles.
- **The organization is deleted.** Its connections and client secrets are removed. A deleted or `orphaned` organization's connections are unavailable meanwhile.
- **The secret leaks or expires.** The company creates a new client secret with its provider and sets it again: `set` without anything else changed. See [If credentials leak](../../../operate/if-credentials-leak/#3-secrets).
- **An unavailable connection looks like a missing one.** Unknown organization, disabled connection, no secret, inactive organization: all answer the same `404`, so the answer reveals nothing.

## Reference

| What | Where |
|---|---|
| Routes for managers: `GET\|PUT\|DELETE /_orgs/{org}/sso[/{provider}]` | [Sign-in connections](../api/#sign-in-connections) |
| Routes for people: `/_auth/orgs/{org}/oauth/{provider}/start\|callback\|id-token`, `/_auth/orgs/{org}/providers` | [Signing in through a connection](../api/#signing-in-through-a-connection) |
| Joining, blocking and what a session remembers | [Joining](../api/#joining) |
| Realm admin: `/_admin/orgs/{org}/sso`, `backd orgs sso\|sso-set\|sso-delete` | [Admin API](../../../operate/administration/admin/), [CLI](../../../operate/administration/cli/) |
| Client: `auth.providers`, `signInWith({ org })`, `orgs.sso(id)`, `NotAllowedError` | [JavaScript client](../../clients/js/#through-an-organizations-own-google-or-microsoft) |
| Audit: `org.sso.set`, `org.sso.deleted`, `org.sso.disabled`, `org.sso.login`, `secret.set` with an `org` | [Audit trail](../../../operate/administration/audit/) |

The errors a sign-in can end with: `not_allowed` (the person is not of the company), `account_exists` (link instead), `link_conflict`, `provider_error` (including a connection backd just disabled), `cancelled`, and `404 not_found` for anything unavailable. For the API: `409 sso_limit` (more connections than the realm allows), `409 org_not_active`, `403 forbidden` (the manager's roles don't allow it), `401 reauth_required` (a change needs a fresh proof) and `503` when the instance has no `BACKD_SECRETS_KEY`.
