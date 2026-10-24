---
title: "24. Live pins"
description: "Make a team's pinboard live: one subscription filtered by team, what a removed member sees, what the stream costs and how to check it with the metrics."
icon: "lightning"
weight: 440
toc: true
---

The team's pinboard (chapter 20) shows what was there when the page loaded. A teammate pins something, and you find out when you reload. A **live list** fixes that: the page loads the pins once and the server tells it about every change after that, to the members who may read them.

## Declare the collection live

One line in `config/shelf/main/pins/collection.yaml`:

{{< example-file path="shelf/main/pins/collection.yaml" lines="6-10" >}}

`realtime: true` is the opt-in: only collections that declare it can be followed, and `backd provision` enables the **pre-change images** MongoDB needs for it (a removal can't be told to the right people without the document as it was). The user in `MONGO_URI` also needs the change stream privilege; the stack's unauthenticated replica set has had it since the first chapter's `compose.yaml`. See [the change feed](../../operate/change-feed/) for what it costs.

## Subscribe, filtered by team

In `app.js` the page keeps one live list of the open team's pins:

```js
const live = pins.inOrg(teamId).subscribe({ load: { sort: '-_meta.created_at', limit: 50 } })
live.on('change', (state) => { this.pinList = state.items })   // sorted, reconciled
live.on('status', (s) => { this.liveStatus = s })              // connecting, live, resyncing
```

`inOrg(teamId)` turns into a `where` on `_meta.org`, so this is a **grouped** subscription. Behind this call the client opens one stream for the whole page, loads the list with a normal read, applies the events that arrived meanwhile (ignoring versions that are not newer) and keeps the list sorted and trimmed to 50. Adding a pin no longer reloads anything: `addPin` calls the `pin` function and the new pin arrives through the stream, for you and for every other member.

The server sends each member only what **their** read rule allows, evaluated on the pin before and after the change: a pin of another team never reaches you, and a member who is removed from the team stops receiving its pins.

## When a member leaves

Remove a member from the team while their page is open. Within a moment their list is **reset**: the server says `access_changed` for the subscription, the client reloads it, finds it may no longer read the team's pins and empties the list (`access_lost`). Nothing the team pins afterwards reaches them. Signing out, or an administrator revoking the session, ends the stream with `session_ended`.

## What it costs, and how to see it

Every change to `pins` is checked against the subscriptions it can matter to. The subscription above has an equality on `_meta.org`, so the server files it under that team and a change only asks the subscriptions of **its** team. A subscription with no equality (`pins.subscribe()` over everything the member can read) is asked on **every** change of the collection on that instance.

You can see the difference in the metrics:

```
backd_realtime_subscription_checks_total{kind="indexed"}   # found by a team (or an id)
backd_realtime_subscription_checks_total{kind="scanned"}   # no key: asked every time
```

Open the board with the team filter and pin something: `indexed` goes up by one per subscriber of that team. Subscribe without the filter and `scanned` grows with every change of every team. At a handful of subscribers it doesn't matter; with thousands, grouped subscriptions keep the checks per change to the team's own members. Other numbers worth watching: `backd_realtime_streams`, and `backd_realtime_resets_total` by reason.

## A live gallery

The gallery is the other list that goes stale: a curator publishes, and members find out on their next reload. Add the same line at the end of `config/shelf/main/assets/collection.yaml`:

{{< example-file path="shelf/main/assets/collection.yaml" lines="72-74" >}}

The app follows what it used to list:

```js
const live = assets.subscribe({ where: { published_at: { $ne: null } }, load: { sort: '-published_at', limit: 9 } })
live.on('change', (state) => { this.assets = state.items })
```

The read rule does the rest: when the curator's `publish` stamps `published_at`, the asset becomes visible under the filter and arrives as `created` for every member; an edit arrives as `updated`, and a delete as `removed`. Drafts never match the filter, so they never reach the gallery.

Two things a list read gave the gallery don't come through the stream. The server sends each asset **as stored**, so there are no `file_links` (the thumbnails) and no `expand` (the author of chapter 21). The app reads them itself: one `assets.list({ where: { id: { $in: ids } }, fileLinks: true, expand })` for the assets it doesn't have them for yet, and again only when an asset's files change, so a new download count costs nothing. File links expire after five minutes, so the app reads the ones on screen again every four. **Load more** follows the list again with a larger `limit`.

A search can't be followed. `$search` isn't one of the operators a subscription accepts (the server decides who sees a change by checking the filter in memory), so typing in the search box turns the gallery into a plain list. Clearing the box makes it live again.

This subscription has no equality on a grouping field, so it is a **scanned** one: it is checked on every change of `assets`. At a gallery's scale that is fine, and `kind="scanned"` in the metrics above shows it.

## You should see

- Two members of a team with the board open: one pins, the other sees it appear; one removes it, it disappears from both.
- A person who is not in the team and opens the same list sees nothing of it, before or after.
- A member removed from the team: their list resets and empties, and later pins don't reach them.
- After signing out in another tab, the page stops being live and asks you to sign in.
- Two members with the gallery open: a curator publishes a draft and it appears in both, thumbnail and author included; a delete removes it. A search shows "search — not live" until it is cleared.

Read [Realtime](../../build/api/realtime/) for the protocol and the limits, and [live lists](../../build/clients/js/#live-lists) for the client.

Next: [chapter 25](../25-private-notes/) — private notes.
