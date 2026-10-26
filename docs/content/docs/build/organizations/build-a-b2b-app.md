---
title: "Build a B2B app"
description: "A step-by-step recipe: declare organization roles, mark collections, write rules, create the organization at sign-up, invite people and switch between organizations."
icon: "briefcase"
weight: 521
toc: true
---

This recipe builds the backend of a small project tracker where each customer is an organization. It uses a realm called `acme` with a `main` database, and the [JavaScript client](../../clients/js/#organizations). The finished version, with a UI, is the [Shelf tutorial's chapter 20](../../../tutorial/20-teams/).

Read the [concepts](../) first if "effective role" or `_meta.org` are new.

## 1. Declare the roles

In `realm.yaml`:

```yaml
auth: enabled

organizations:
  roles:
    owner:  { description: Runs the organization, manage: [org, members, roles, teams] }
    admin:  { description: Invites and removes people, manage: [members, teams] }
    editor: { description: Creates and edits projects }
    member: { description: Reads }
  default_roles: [member]
  creator_roles: [owner]
  invitation_link: "https://app.acme.example/join?token={token}&org={org}"
```

- The `manage` capabilities decide who administers the organization; `owner` needs `org`, since an organization always keeps someone who can manage it.
- `default_roles` are held by every member, so `member` is never granted one by one.
- The `invitation_link` must hold `{token}` and `{org}` (the app learns which organization to sign in to from it) and be inside the realm's [`sign_in.allowed_redirects`](../../configuration/realm/).
- Creating organizations is allowed to anyone with a verified email by default (`create: user.email_verified`).

## 2. Mark the collections

In `main/projects/collection.yaml`:

```yaml
organization: required

on_org_delete:
  action: delete
```

`required` means every project belongs to one organization. `on_org_delete` is what happens to the projects when the organization is deleted: `delete` removes them (the other choice is `keep`). When a person is erased, the projects they made stay with the organization, with the owner cleared; nothing to declare.

## 3. Write the rules

```yaml
rules:
  read:   inOrg(document._meta.org)
  create: hasOrgRole(data._meta.org, 'editor') || hasOrgRole(data._meta.org, 'owner')
  update: hasOrgRole(document._meta.org, 'editor') || hasOrgRole(document._meta.org, 'owner')
  delete: hasOrgRole(document._meta.org, 'owner')
```

The read rule becomes "`_meta.org` is one of the caller's organizations" inside the query, so a list shows only the organizations the caller belongs to. More patterns in the [rules cookbook](../rules-cookbook/).

## 4. Create the organization at sign-up

A new person starts a company in one call: the account, the organization (they become its `owner`) and a session scoped to it:

```js
const backd = createClient({ url, realm })
await backd.auth.signUpWithOrg({ email, password, name: 'Acme Corp', slug: 'acme-corp' })
```

The `slug` is how people name the company when they sign in, so it is required and unique. Someone who already has an account creates another company from a session of theirs, with `backd.orgs.create({ name, slug })`; it answers a second session, scoped to the new one, which the client keeps. A company is limited by `max_created_per_user` (5 by default). If you'd rather create organizations from a function, for a plan or a payment, set `create: "false"`: [functions](../functions/) and realm administrators can always create.

## 5. Create and read documents in an organization

```js
const projects = backd.db('main').collection('projects')
await projects.create({ title: 'Redesign the site' })
const { items } = await projects.list()
```

A session works in the organization it was opened for, so there is nothing to say: creates go there, and lists show only its documents (the rules still decide which). To work in another organization the person signs in to that one.

## 6. Invite people

```js
const inv = await backd.orgs.invitations(org.id).create({
  binding: 'email', email: 'ana@example.com', roles: ['editor'], send: true,
})
```

`send: true` emails the link (the `org-invitation` email, which you can [customize](../../functions/email/)); the answer also carries the `link`, shown once, for a chat or a QR code. For a shared link, use `binding: 'none'`.

The invitee follows the link into your app (`invitation_link` in `realm.yaml` carries the token and the organization), which asks what it offers and signs them in with it:

```js
const offer = await backd.auth.previewInvitation(token, { org })   // { org, roles, teams }: "Join Acme as editor?"
await backd.org(org).auth.login({ email, password, invitation: token })   // or signUp, for a new account
```

Taking the invitation is part of signing in to the organization. An unknown, expired or used token previews as `400 invalid_invitation`, always the same; signing in with one that is not the person's is the uniform `401 invalid_credentials`.

## 7. Switch between organizations

A person may belong to several, and holds one session for each they signed in to. The client keeps them:

```js
const mine = await backd.orgs.signedIn()       // [{ ref, id, name, expiresAt }]: what a switcher lists
const acme = backd.org('acme-corp')            // a client whose calls use that organization's session
await acme.db('main').collection('projects').list()
```

Switching is choosing which client to use; what a person can see is decided by the rules at each request, in the organization the session belongs to. For an organization they have no session in, the app sends them to its sign-in page: see [the sign-in experience](../sign-in-experience/).

## 8. Manage people

```js
await backd.orgs.members(org.id).list()                         // roles and where they come from
await backd.orgs.members(org.id).update(userId, ['admin'])      // direct roles
await backd.orgs.teams(org.id).create({ name: 'Editors', roles: ['editor'] })
```

An administrator (`manage: members`) can give only roles that open no capability they lack, and can't change an owner. See [teams or direct roles](../#direct-roles-or-teams).

## What you have

Every customer is isolated by the database query itself, people join by invitation, access ends the moment someone leaves, and no handler was written. Next: limit by plan with [claims](../rules-cookbook/#limit-by-plan-with-claims), require a second factor per organization and check [what happens when](../what-happens-when/) a member leaves or an organization is deleted.
