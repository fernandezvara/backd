---
title: "Authentication strategies"
description: "How a realm decides who may sign in and with what: strategies in plain words, the common setups, and roles that depend on how a person signed in."
icon: "path"
weight: 505
toc: true
---

A **strategy** is a name for one way to sign in: a list of **methods** that a person completes, one after the other. The first method says who they are; the ones after it add proof. A realm lists its strategies under `auth.strategies` in `realm.yaml`, and the app (or `backd login --strategy`) says which one it uses.

```yaml
auth:
  strategies:
    password: [password]               # email and password
    admin_password: [password, totp]   # then a code from an authenticator app
    google: [google]                   # a provider declared in auth.providers
```

The **methods** are:

| Method | What the person does | Can be |
|---|---|---|
| `password` | types their email and password | the first method |
| `magic_link` | uses a link or code sent to their address ([Magic links](../magic-links/)) | the first method |
| a provider's name (`google`, `microsoft`, `apple`) | signs in at the provider ([Sign in with a provider](../providers/)) | the first method |
| `sso` | signs in through their organization's own connection ([Organization sign-in](../../../operate/organization-sign-in/)) | the first method |
| `passkey` | uses a passkey on their device, a password manager or a security key ([Passkeys](../sessions/#passkeys)) | the first method |
| `totp` | types the code of an authenticator app, or a recovery code ([Multi-factor authentication](../mfa/)) | a later method |

A passkey can also be a **later step** (`admin_passkey: [password, passkey]`): after the password, the challenge says `next: ["passkey"]` and the app finishes the sign-in with the passkey, as it would with a code; a person who has none is led to add one as part of signing in. The session records `methods: ["password", "passkey"]`.

## The common setups

**Password only.** The smallest realm, and the one a development setup wants:

```yaml
auth:
  strategies:
    dev: [password]
```

With a single strategy nobody has to name it: every sign-in uses it.

**Password for users, password and a code for administrators.** Two strategies, and the administrator role accepts only the strong one:

```yaml
auth:
  strategies:
    users: [password]
    admin_password: [password, totp]

roles:
  admin:
    admin: true
    strategies: [admin_password]   # active only in a session made with this strategy
```

Both strategies start with the password, so the app now says which it means: `backd login --strategy admin_password`, `signIn({ email, password, strategy: 'admin_password' })` in JavaScript, and the same option in the other clients. `GET /_auth/strategies` lists the names so an app can draw its sign-in screen, and the admin interface offers them in a list.

**Passkeys for users.** `users_passkey: [passkey]` lets people sign in without typing anything: the browser offers the passkeys it holds, and a verified passkey counts as two factors on its own (`mfa` is true). The realm turns passkeys on with `auth.passkeys`.

**Providers next to the password.** `google: [google]` lets people sign in with Google, `admin_google: [google, totp]` asks administrators for a code after it.

**A different strategy per client.** A web app, a console and a mobile app can each use the strategy that suits them, and the app chooses it. backd does **not** know which client a request comes from (headers can be forged), so a strategy is not a lock on a client. What protects something is the **role**: a role that lists only strong strategies cannot be held in a session made with a weak one, whoever asked.

## Roles and strategies

A role may list the strategies it accepts. It is **active** in a session only if the session's strategy is in that list; a role with no list is active in every strategy. Everything that decides access reads the active roles: `user.roles` in [rules](../rules/), `ctx.user.roles` in functions, the admin API and the admin interface.

A worked example. The realm above, plus an `editor` role that lists nothing, and three people:

| Person | Holds | Signs in with | Session |
|---|---|---|---|
| Ana | `admin` and `editor` | `admin_password` | `admin` and `editor` active |
| Ana | `admin` and `editor` | `users` | `editor` active; `admin` **inactive** |
| Ben | only `admin` | `users` | signed in with **no** active roles; the answer says `roles_inactive: ["admin"]` |
| Cy | no role | either | signed in; every strategy is open to someone with no role |

A person who signs in with a strategy their role does not list is **not refused**. Refusing would tell anyone who guesses an address which roles it holds, and would lock out people who hold both a strong and a weak role. The session is simply less powerful, and the sign-in answer lists `roles_inactive` (only after the person has authenticated), so an app can say "sign in with your authenticator to use the admin role".

```json
{
  "token": "bds_…",
  "strategy": "users",
  "roles_inactive": ["admin"],
  "user": { "id": "…", "email": "ben@example.com", "roles": [] }
}
```

{{< hint style="note" >}}
Listing is explicit. A stronger strategy does not carry a weaker one's roles: in the example, an `admin_password` session holds `editor` only because `editor` lists no strategies. If `editor` listed `users`, administrators would need to be added to its list too.
{{< /hint >}}

## What a session records

A session records its `strategy`, its `methods` (the steps completed, in order) and `mfa` (true when two different kinds of method were used). Rules and functions see them as `user.strategy`, `user.methods` and `user.mfa`:

```yaml
rules:
  delete: user != nil && user.strategy == "admin_password"
```

Refreshing a session keeps all three. Taking a strategy out of the configuration takes effect at the next sign-in, and existing sessions lose any role whose list no longer allows their strategy.

## Steps

After the first method, a strategy answers with a **challenge** for the next one. `POST /_auth/mfa/verify` trades the challenge and the code for the next challenge or the session; a strategy of three steps simply asks twice. A person who has no authenticator yet is led to set one up as part of signing in, and the sign-in ends with the session and their recovery codes.

**The strategy decides the steps, not the user.** A person who set up an authenticator is asked for it only by the strategies that list `totp`. A realm that wants the code from everyone writes it into the strategy (`users: [password, totp]`).

## Choosing a strategy

| The call | Answer |
|---|---|
| names a strategy that exists and starts with this method | the sign-in goes on |
| names none, and exactly one strategy starts with this method | that one is used |
| names none, and several start with it | `400 strategy_required` |
| names one that does not exist, or one that starts with another method | `400 unknown_strategy` (the same answer for both) |

The `strategy` goes in the body of `POST /_auth/login` and `POST /_auth/magic-link`, in the query or body of the provider start route, and in the body of the ID-token routes; the organization routes under `/_auth/orgs/{org}/…` take it the same way. Sign-up is always by password and uses the first strategy that starts with it.

## Moving from the old keys

Before strategies, `realm.yaml` had `auth: enabled` and the sign-in settings at the top level. They moved under `auth:`; the table is in the [upgrade notes](../../../operate/#upgrading-to-the-strategy-release) and the [changelog](https://github.com/fernandezvara/backd/blob/main/CHANGELOG.md). A realm that still uses the old keys stops at startup with a message naming the new place.
