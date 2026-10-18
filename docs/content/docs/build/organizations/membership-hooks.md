---
title: "Membership hooks"
description: "Run an internal function when people join, leave or change roles in an organization: onboarding, offboarding and syncing roles, with events that carry a sequence number."
icon: "webhooks-logo"
weight: 524
toc: true
---

Apps often need to react when someone joins a company: create a profile, add them to a channel, send a welcome. Polling for new members is slow and misses changes; **membership hooks** call an [internal function](../../functions/internal/) for you, in the background, when it happens.

```yaml
# realm.yaml
organizations:
  hooks:
    on_member_joined: core/members-joined     # <database>/<function>
    on_member_left:   core/members-left
    on_roles_changed: core/roles-changed
```

Every hook is optional, and each names a function that is **`internal: true`** and **`mode: async`**; startup refuses anything else. They are run through the job queue like [`account.on_signup`](../../auth/providers/#the-profile-and-the-sign-up-hook): there is **no user in `ctx`**, the function's `retry:` applies, and a failure never undoes the change that caused it.

## What each one receives

| Hook | When | Input |
|---|---|---|
| `on_member_joined` | an invitation is accepted, a member is added directly (administrator or function), or the creator of an organization joins it | `event`, `org`, `user`, `seq`, `invitation` (the id, when there was one), and what the member holds: `roles` (direct), `teams`, `team_roles` (with their windows) and `effective_roles` |
| `on_member_left` | the member leaves, is removed, or their account is [erased](../../../operate/administration/erasure/) | `event`, `org`, `user`, `seq`, `reason`: `left`, `removed` or `erased` |
| `on_roles_changed` | a member's direct roles, teams or [team roles](../team-roles/) change, or the roles a team carries change (one event per member of the team) | `event`, `org`, `user`, `seq`, `before` and `after`, each with `roles`, `teams`, `team_roles` and `effective_roles` |

- **An event is queued with the change, or not at all.** The membership change and its event are written in one database transaction: if the event can't be queued, the change is not made and the call fails, so nothing changes without its hook and nothing is announced that did not happen. Events carry a job id made of the event (organization, user, `seq`), so queuing one twice is one job.
- **Nothing changed, no event.** Setting the roles a member already has is not a change.
- **Windows are not events.** A grant with `from` and `until` is reported when it is *given*, with its window; backd does not call a hook when the window opens or closes.
- **Tokens and content never travel:** an event holds ids and role names.

## Order: use `seq`

The job queue runs jobs as workers are free, so two events about the same person may run in either order, or one may be retried after the next ran. Every event carries `seq`, a number that grows for each organization and user (it does not restart when the person leaves and joins again). A handler that must not apply a stale event keeps the last `seq` it applied:

```ts
export default async function (ctx, input) {
  const state = await ctx.admin.db("main").collection("member_state").get(`${input.org}:${input.user}`).catch(() => null);
  if (state && state.seq >= input.seq) return;        // an older event arriving late: drop it
  // ...apply the change, then store input.seq
}
```

Make handlers **idempotent** too: a retry runs the same event again.

## Recipe: onboarding and offboarding

- **`on_member_joined`:** create the person's workspace document (`ctx.admin.db`), subscribe them to the company's channels, and send a welcome with `ctx.email.send`. The event says which roles and teams they arrived with, so a department's checklist can start from the team.
- **`on_roles_changed`:** compare `before.effective_roles` with `after.effective_roles` to grant or revoke the access you mirror elsewhere (a billing seat, a Slack group).
- **`on_member_left`:** archive what they own. With `reason: "erased"` the user no longer exists: don't try to read them.

{{< hint style="note" >}}
The hooks run after the change is stored. If your function needs the state *at that moment*, read it from the event: the membership may have changed again by the time the job runs.
{{< /hint >}}
