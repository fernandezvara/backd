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
    owner:  { description: Runs the company, manage: [org, members, roles, teams] }
    admin:  { description: Invites and removes people, manage: [members, teams] }
    editor: { description: Creates and edits projects }
    member: { description: Reads }
  default_roles: [member]
  creator_roles: [owner]
  invitation_link: "https://app.acme.example/join?token={token}"
```

- The `manage` capabilities decide who administers the organization; `owner` needs `org`, since an organization always keeps someone who can manage it.
- `default_roles` are held by every member, so `member` is never granted one by one.
- The `invitation_link` must be inside the realm's [`sign_in.allowed_redirects`](../../configuration/realm/).
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

After the user signs up (and verifies their email), the app creates their organization. They become its `owner`:

```js
await backd.auth.signup({ email, password })
// ...verify the address, sign in...
const org = await backd.orgs.create({ name: 'Acme Corp' })
localStorage.setItem('org', org.id)
```

Creating needs a verified address because the realm's `create` rule says so, and is limited by `max_created_per_user` (5 by default). If you'd rather create organizations from a function, for a plan or a payment, set `create: "false"`: [functions](../functions/) and realm administrators can always create.

## 5. Create and read documents in an organization

```js
const projects = backd.db('main').collection('projects').inOrg(org.id)
await projects.create({ title: 'Redesign the site' })
const { items } = await projects.list()
```

`inOrg(id)` sends `?org=` on the calls. Without it a create answers `400 org_required`. Someone who isn't a member gets `403 not_org_member`, the same for an organization that doesn't exist.

## 6. Invite people

```js
const inv = await backd.orgs.invitations(org.id).create({
  binding: 'email', email: 'ana@example.com', roles: ['editor'], send: true,
})
```

`send: true` emails the link (the `org-invitation` email, which you can [customize](../../functions/email/)); the answer also carries the `link`, shown once, for a chat or a QR code. For a shared link, use `binding: 'none'`.

The invitee follows the link into your app, which reads the token and, once signed in:

```js
const offer = await backd.orgs.invitations.preview(token)   // { org, roles, teams }: "Join Acme as editor?"
await backd.orgs.invitations.accept(token)
```

An unknown, expired or used token answers `400 invalid_invitation`, always the same; an email invitation for another address answers `403 invitation_not_for_you`.

## 7. Switch between organizations

A person may belong to several. The app lists them and keeps the one selected:

```js
const mine = await backd.orgs.list()          // each with the caller's effective roles
const current = mine.find((o) => o.id === localStorage.getItem('org')) ?? mine[0]
const projects = backd.db('main').collection('projects').inOrg(current.id)
```

The selection is only the app's choice of `?org=`; what a person can see is decided by the rules at each request. A list without `?org=` shows the documents of all their organizations.

## 8. Manage people

```js
await backd.orgs.members(org.id).list()                         // roles and where they come from
await backd.orgs.members(org.id).update(userId, ['admin'])      // direct roles
await backd.orgs.teams(org.id).create({ name: 'Editors', roles: ['editor'] })
```

An administrator (`manage: members`) can give only roles that open no capability they lack, and can't change an owner. See [teams or direct roles](../#direct-roles-or-teams).

## What you have

Every customer is isolated by the database query itself, people join by invitation, access ends the moment someone leaves, and no handler was written. Next: limit by plan with [claims](../rules-cookbook/#limit-by-plan-with-claims), require a second factor per organization and check [what happens when](../what-happens-when/) a member leaves or an organization is deleted.
