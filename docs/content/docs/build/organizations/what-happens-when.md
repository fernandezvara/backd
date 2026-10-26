---
title: "What happens when…"
description: "The consequences of leaving, erasure, deletion, transfers and role changes in an organization, plus the limits and the errors to expect."
icon: "question"
weight: 523
toc: true
---

## What happens when…

| Event | Access | Documents and data |
|---|---|---|
| **A member leaves or is removed** | Ends at the next request. They leave their teams | Documents they created stay with the organization. The last manager can't leave: `409 last_org_manager` |
| **A member is erased** | Memberships, team memberships and bound invitations are removed | Documents stay, owner cleared (or erased, with `in_org: apply`). An organization left without a manager becomes `orphaned` |
| **The last manager is erased** | The organization has nobody to manage it | `orphaned`: a realm administrator assigns an owner (`backd orgs assign-owner`). The startup log and the erase report name it |
| **An organization is deleted** | Ends at once for everybody; the slug is free | Each collection's `on_org_delete` applies in a resumable job: `delete` removes the documents and queues their files, `keep` leaves them readable only by administrators |
| **A document is transferred** | The new organization's members see it, the old one's don't, at once | Only realm administrators transfer ([`_transfer`](../../api/documents/#fixing-data-as-an-administrator)); its `_meta.org` is the only change |
| **`sso_required` is turned on** | The other ways in answer `400 sso_required`; the organization's sessions that were not opened through a connection end | Nothing changes. Members with no identity on the connection can't come in until they use it. Removing the last working connection turns it off |
| **`require_mfa` is turned on** | Sessions without a second factor stop counting as members there; its routes answer `403 org_requires_mfa` | Nothing changes. The manager's own session needs a second factor, or they would lock themselves out |
| **A role is removed from a team** | Members lose it at the next request, unless they hold it directly or through another team | The members list shows what remains. A team can't lose its last managing role if that leaves nobody who can manage |
| **A role is no longer declared in `realm.yaml`** | The stored grants stay, and grant nothing; startup logs them | Declare the role again to restore it, or give another one |
| **A member's roles change** | At the next request: there is no cache | Nothing |
| **An invitation is revoked or its link reissued** | The old token is invalid at once | People who already accepted keep their membership |

See also [teams or direct roles](../#direct-roles-or-teams) for why the same role should be given one way.

## Limits

| Limit | Default | Setting |
|---|---|---|
| Organizations a user may create (deleted ones count) | 5 | `max_created_per_user` |
| Organizations a user may belong to | 100 (the most) | `max_memberships_per_user` |
| Teams per organization | 100 | `max_teams_per_org` |
| Teams per member | 20 | `max_teams_per_member` |
| Pending invitations per organization | 500 | `max_pending_invitations` |
| Claims of an organization | 1 KiB | not configurable |
| Lifetime of an open invitation link | 30 days at most | `expires_in` |
| Slug | lowercase letters, digits and `-`, 63 characters | |

Realm administrators and functions are not bound by `max_created_per_user`.

## Errors

| Status and `code` | When | What to do |
|---|---|---|
| `400 org_required` | a key acting for nobody, or a user with no organization in the key, creating in a collection with `organization: required` | sessions always have one; bind the key to one (`--org`) |
| `400 validation_error` on `org` (`org_not_allowed` in a collection without organizations) | `?org=` on a session's request | remove it: the session decides |
| `401 session_unscoped` | a session from before sessions were scoped | sign in again, to an organization |
| `400 immutable_field` | `PUT` or `PATCH` changes `_meta.org` | don't; transfers are for administrators |
| `400 invalid_invitation` | unknown, expired, revoked or used token | ask for a new invitation |
| `403 not_org_member` | not a member, or the organization doesn't exist (the same answer) | check the id; join first |
| `403 org_requires_mfa` | the organization requires a second factor and the session has none | start the [enrollment](../../auth/mfa/) |
| `403 org_role_requires_mfa` | the action needs a [role that requires a second factor](../team-roles/#roles-that-require-a-second-factor); `details` names it | start the enrollment, or sign in again with a second factor |
| `403 invitation_not_for_you` | the invitation is bound to another user or address | sign in with the invited account |
| `403 forbidden` | the caller's roles don't allow the change | ask a manager with the role |
| `409 last_org_manager` | the change leaves nobody who can manage | give `manage: org` to someone else first |
| `409 slug_taken` | the slug belongs to a live organization | choose another |
| `409 org_limit_reached` | the user created the most organizations they may | delete one or raise the cap |
| `409 team_limit_reached`, `409 team_name_taken` | team caps and duplicate names | |
| `409 invitations_limit` | too many pending invitations | revoke old ones |
| `409 org_not_active` | the organization is deleted | administrators can reactivate it |

In the [JavaScript client](../../clients/js/#organizations) the second factor, non-member and last-manager cases are `OrgRequiresMFAError`, `NotOrgMemberError` and `LastOrgManagerError`.
