---
title: "20. Teams as organizations"
description: "Let people create teams, invite each other with a link or an email, keep a team's pins to its members, give roles inside a team, limit a team's pins by plan with a claim, and delete a team with its report first."
weight: 400
toc: true
---

Until now Shelf was one workspace. Real groups want their own corner: *this* team's pins, readable by *this* team and nobody else, with a person who runs it and people who only look. That is what an [organization](../../build/organizations/api/) is: a team is an organization of the realm, created by whoever starts it, and the rules read who belongs to which.

The old way to do this is an array of member ids on every document (or a `members` collection to keep in sync). It works until someone leaves: their id stays in the arrays until something removes it, and every rule and every query has to know about the array. An organization has none of that: **access ends the moment someone leaves**, because there is nothing to keep in sync.

## Declare the roles

A team's roles are not the realm's roles (`admin`, `curator`): they mean something only inside one team. `realm.yaml` declares them, with what each may manage:

Add to `config/shelf/realm.yaml`:

```yaml
organizations:
  roles:
    owner:  { description: Runs the team, manage: [org, members, roles, teams] }
    admin:  { description: Invites and removes people and deletes any pin, manage: [members, teams] }
    editor: { description: Adds and edits pins }
    member: { description: Reads the team's pins }
  default_roles: [member]
  max_created_per_user: 5
  invitation_link: http://localhost:8080/?join={token}
```

- `owner` runs the team: rename it, its members, which roles they hold, and delete it.
- `admin` invites and removes people (and, in the rules below, deletes any pin), but can't give or take `owner`, or remove an owner.
- `editor` and `member` manage nothing; `member` is held by everyone without being granted (`default_roles`).
- `invitation_link` is where an invitation's link points: the app, which reads `?join=` and offers it.

## A collection that belongs to teams

Pins are what a team shares. The collection says every pin belongs to a team and what happens to them when the team is deleted:

{{< example-file path="shelf/main/pins/collection.yaml" >}}

`_meta.org` holds the team. The schema has no team field of its own: backd sets it at creation from `?org=`, and nobody can change it afterwards. The `read` rule is a **database filter**: a list shows a member only the pins of the teams they belong to, and counts and pages follow.

There is no `create` rule: clients can't make pins. The `pin` function does, and that is how the plan's limit is enforced.

## The plan, and the function that counts

Rules can't count, so a limit lives in a function. A team's **claims** are facts about it that only realm administrators and functions write; `plan` is one. The `pin` function reads it from `ctx.user.orgs`, counts the team's pins with the admin client, and refuses past the limit:

{{< example-file path="shelf/main/_functions/pin/index.ts" >}}

An operator puts a team on a plan with another function, the only place the claim is written:

{{< example-file path="shelf/main/_functions/team-plan/index.ts" >}}

## The app

The Teams view creates a team, shows the open team's pins and members, and invites. A pin goes through the function; reading uses the collection scoped to the team:

```js
const team = await backd.orgs.create({ name: 'Docs' })               // the caller is the owner
await main.fn('pin', { org: team.id, title: 'Handbook', url })       // the plan decides
const { items } = await pins.inOrg(team.id).list()                   // ?org= is a filter
```

An invitation answers its link **once**, as a link to copy, and can also be emailed:

```js
const invite = await backd.orgs.invitations(team.id).create({ binding: 'email', email, roles: ['editor'], send: true })
invite.link   // http://localhost:8080/?join=bdo_…
```

The person who opens it signs in, sees what it offers (`invitations.preview`) and takes it (`invitations.accept`). Anybody who holds a link can use it, so an *open* link never gives a role that manages the team.

## Roles directly, access through teams

Inside a team, **teams** are groups of members that carry roles. Give management roles (`owner`, `admin`) directly, and access roles through a team:

```js
const editors = await backd.orgs.teams(team.id).create({ name: 'Editors', roles: ['editor'] })
await backd.orgs.teams(team.id).addMember(editors.id, userId)
```

A member who holds `editor` both ways keeps it after being taken out of the team: removal looks done and isn't. The members list shows where each role comes from, and marks a role held twice.

## A second factor, the last owner, deleting

- **`require_mfa`.** An owner can require a second factor of the team's members (`orgs.update(id, { require_mfa: true })`, with a fresh proof, from a session that has one). A session without it stops counting as a member there; the app asks for one (`mfa_met` is false).
- **The last owner stays.** `leave` and any change that would leave nobody who can manage the team answer `409 last_org_manager`. Make someone else an owner first.
- **Deleting a team.** An owner deletes it with a fresh proof; an operator looks first at what it holds: `backd orgs owned --realm shelf --id <id>`. Access ends at once and a job removes its pins (`on_org_delete: delete`).

## You should see

- Creating a team makes you its owner; another member joins with the link and reads the team's pins; a stranger can't read the team or pin into it (`not_org_member`).
- An editor pins and removes their own pins; only an admin removes other people's.
- The sixth pin of a free team is refused (`plan_limit`); after `team-plan` puts it on `pro`, it fits.
- The only owner can't leave (`last_org_manager`); with another owner, leaving ends access at once.
- Deleting the team removes its pins, and `backd orgs owned` counted them first.

Read [Organizations](../../build/organizations/api/) for the API, [Organizations in functions](../../build/organizations/functions/) for `ctx.user.orgs` and `ctx.admin.orgs`, and [the rules](../../build/auth/rules/#organizations) for `inOrg` and `hasOrgRole`.
