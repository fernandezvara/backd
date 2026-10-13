---
title: "Records, test send and metrics"
description: "Answer 'did my push go out, and why not' from a send's record, try a push on one user, and watch the counters."
icon: "chart-bar"
weight: 583
toc: true
---

## The record of a send

Every send leaves one record, kept for `push.send_retention` (30 days by default). It holds **counts, never content**: no title, body, data or token, and no user ids (those live only in the send's jobs, which follow the realm's [job retention](../../functions/jobs/)).

| Field | Meaning |
|---|---|
| `id`, `kind`, `sender`, `created_at` | The send, its template kind (`raw` for a message the function wrote), who sent it (`function:<database>/<name>` or `admin:<actor>`) and when |
| `users` | How many users were named; `null` for an audience |
| `queued`, `limited`, `no_devices` | Users handed to delivery, users over their limit, users with no device to reach (a silent push skips browsers) |
| `sent`, `invalid`, `failed` | Devices the provider accepted, devices removed as dead, devices not delivered to |
| `planned`, `batches`, `batches_done`, `units`, `units_done` | How far the work is |
| `completed`, `completed_at` | Everything is done |

```sh
backd push sends --realm acme                          # the latest, newest first
backd push sends --realm acme --kind order-shipped
backd push sends --realm acme --function main/notify --limit 10
backd push sends --realm acme --id ps_d3c9ljp8hc2g00b6s1m0
```

The admin API: `GET /_admin/push/sends` (filters `kind`, `function`, `since`, `until`, `limit`, `skip`) and `GET /_admin/push/sends/{id}`. Both need the `functions` area, and read-only administrators may use them.

## Why a push didn't arrive

| What the record says | What it means |
|---|---|
| `limited` > 0 | The user was over `push.limits.per_user`. Nobody is exempt, test sends included |
| `no_devices` > 0 | The user has no device whose session is alive (or only browsers, for a silent push) |
| `invalid` > 0 | The provider said the token is dead; the device was removed and the app registers again at its next launch |
| `failed` > 0 | The provider refused it for good, or the unit ran out of attempts. The worker's log says why, without tokens |
| not `completed` | Batches or units are still running, or waiting out a retry backoff |

## The test send

```sh
backd push test --realm acme --email ana@example.com --title "Test" --body "Does it ring?"
backd push test --realm acme --email ana@example.com --kind order-shipped --data '{"order_no": "A-17"}'
```

`POST /_admin/push/test` sends to **one user**, rendered and checked like any push. It needs the `users` area, is audited as `push.test`, and **counts toward the user's limits**: there is no administrator bypass. The answer says how many were `queued`, `limited` or had `no_devices`; follow the delivery with `backd push sends --id`.

## Metrics

On the metrics endpoint, labeled by realm:

| Metric | Labels | Counts |
|---|---|---|
| `backd_push_users_total` | `outcome`: `queued`, `limited`, `no_devices` | Users, as batches are planned |
| `backd_push_deliveries_total` | `status`: `sent`, `invalid`, `failed` | Devices, as units are delivered |
| `backd_push_devices_pruned_total` | `reason`: `invalid`, `stale`, `session`, `cap`, `moved` | Devices removed from the registry |

A worker that dies mid-send is counted again when its batch runs again, so these are for rates and alerts, not accounting: the records are exact.
