---
title: "Examples"
description: "Example applications built on backd, and what each one shows."
icon: "apps"
weight: 700
toc: true
---

The repository includes example configurations under `examples/config`, and example applications built with the [JavaScript client](../clients/js/):

| Example | Realm | Shows |
|---|---|---|
| [Blog](blog/) | `blog` | Sign-up, sessions, public posts and private drafts, ownership, posts signed with the writer's own email, filters, conflict-free edits, and a [sample function](blog/#a-server-side-function-stats) for counts a reader can't compute themselves |
| [Expenses without functions](expenses/) | `expenses` | Shared data between users, invitations by email, and the limits of access rules: what they can enforce, and the holes only a server-side function can close |
| [Functions cookbook](../functions/cookbook/) | `workshop` | A small shop with six tested functions: a sync call as the caller, a privileged idempotent refund, a background report job, a nightly cleanup and a daily digest on cron schedules, and a payment webhook |
| [Expenses with functions](expenses-with-functions/) | `expenses-with-functions` | The same app, same layout, with those holes closed by five functions — compared side by side with what changed and why |

`make example` serves the configurations and the applications on `https://localhost:8443`, with an index of the applications at `/example/` (see [Getting started](../getting-started/)).
