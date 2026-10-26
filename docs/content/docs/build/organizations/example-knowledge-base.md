---
title: "Example: a knowledge base"
description: "Documentation storage for organizations: company pages, team pages that are private, shared with the organization or public, and drafts. A tested realm."
icon: "book-open"
weight: 528
toc: true
---

A place where organizations keep their documentation: the company's pages (a handbook, policies), and each team's own, which the team decides to keep to itself, share with the rest of the organization or publish to the world. The whole realm is `examples/config/kb`, and its rules are tested with [`backd rules test`](../../auth/rules/#testing-rules).

## Who is who

| Who | Is | Can |
|---|---|---|
| Olga | `owner` of the organization | write the company's pages; read every published page |
| Ana | member of Platform, `editor` there | write, publish and decide who reads Platform's pages |
| Ben | member of Platform | read Platform's pages, the organization's and the public ones |
| Carla | member of Support, `contributor` in Platform for two weeks | start drafts and edit the text of Platform's pages |
| Dan | member of Support | read what is shared with the organization and Support's pages |
| A visitor | nobody, no account | read the public pages |

## The realm

{{< example-file path="kb/realm.yaml" >}}

- `editor` **requires a second factor**: without one it only counts as a plain member.
- `editor` can **grant `contributor`**, in its own team.
- There is no role for "reads the team's pages": `inTeam` says it.

## The collection and its rules

{{< example-file path="kb/main/pages/collection.yaml" >}}

- **A page with no team is the organization's.** `team: optional` lets the same collection hold the company's pages and each team's. A team page is created with `?team=`.
- **`visibility`** decides who reads a published page: `team` (its members, editors and contributors), `org` (every member of the organization) or `public` (anyone, signed in or not). Owners read every published page of the organization.
- **Drafts** are the first line of `read`: whoever created a page reads it whatever its status, and nobody else reads a draft, owners included. Publishing is changing `status`, which only editors (and owners, for company pages) do.
- **`read`** is filtered inside the query: Dan's list never contains Platform's private pages, so nothing is received to be hidden later.
- **`update`:** contributors and authors edit the text and leave `status` and `visibility` as they are. `delete` lets authors discard their own drafts.
- **`on_team_delete: keep`**: when a team is deleted its pages stay, with the old team id, readable by their visibility and by the owners.

{{< hint style="note" >}}
`public` means anyone **without an account**, and the members of the page's own organization. A signed-in member of another organization doesn't see it: a session reaches the organization it was opened in, and nothing else. A public site reads the pages without signing in.
{{< /hint >}}

## Giving and ending cover

```http
POST /v1/acme/_orgs/{org}/teams/{platform}/roles          # as Ana
{ "user": "<carla>", "role": "contributor", "from": "2026-08-01T00:00:00Z", "until": "2026-08-15T00:00:00Z" }
```

From 1 August Carla's lists include Platform's pages and she can write drafts there; on the 15th that is over, with nothing to clean up. Ana can end it earlier with `DELETE` on the same path.

## The tests

{{< example-file path="kb/main/pages/rules.test.yaml" >}}

The fixture says what each person is in `orgs`: roles, teams and team roles, as the server would load them for a request. A contributor whose window has closed is simply a user without that role, and the visitor is the `anonymous` caller every fixture has.

## Around it

- **Onboarding:** an `on_member_joined` [hook](../membership-hooks/) can subscribe a new member to their team's pages.
- **Emails in the organization's name:** with [organization context](../organization-context/) and [organization templates](../email-templates/), the "page published" email carries the organization's brand, and each organization edits its own text.
- **The app:** `client.db('main').collection('pages').inTeam(team).create({...})` in the [JavaScript client](../../clients/js/#organizations); the team's editors come from `client.orgs.teams(org).members(team, { role: 'editor' })`.
