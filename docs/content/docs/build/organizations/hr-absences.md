---
title: "Example: HR absences"
description: "A company's absence module built with team roles: employees ask, department managers and delegates decide, HR sees everything. A tested realm."
icon: "calendar-x"
weight: 528
toc: true
---

An HR module for a B2B app: each company is an organization, each department a team, and people ask for absences that their manager (or someone covering) approves. The whole realm is `examples/config/hr`, and its rules are tested with [`backd rules test`](../../auth/rules/#testing-rules).

## Who is who

| Who | Is | Can |
|---|---|---|
| Ana | member of Sales | ask for an absence; edit or delete it while it is requested |
| Marta | member of Sales, `manager` there | see and decide Sales' absences |
| Dario | member of Support, `delegate` in Sales for two weeks | see and decide Sales' absences in that period |
| Hugo | `hr` in the company | see every absence of the company |

## The realm

{{< example-file path="hr/realm.yaml" >}}

- `manager` and `hr` **require a second factor**: without one they only count as plain members.
- `manager` can **grant `delegate`**, in its own department.
- There is no role for "sees the department": `inTeam` and the team roles say it.

## The collection and its rules

{{< example-file path="hr/main/absences/collection.yaml" >}}

- **`read`** is filtered inside the query: Ana's list is her own absences; Marta's is Sales'; Hugo's the company's. Nobody receives a document to hide it later.
- **`create`** needs the department in the request (`?team=`) and a team the person belongs to, and an absence is born `requested`.
- **`update`** lets the employee edit their own request and nothing more, and lets a manager or delegate decide.
- **`on_team_delete: keep`**: when a department is deleted its absences stay, with the old team id, readable by HR.

## Giving and ending cover

```http
POST /v1/acme/_orgs/{org}/teams/{sales}/roles          # as Marta
{ "user": "<dario>", "role": "delegate", "from": "2026-08-01T00:00:00Z", "until": "2026-08-15T00:00:00Z" }
```

From 1 August Dario's list of absences includes Sales'; on the 15th it doesn't, with nothing to clean up. Marta can end it earlier with `DELETE` on the same path.

## The tests

{{< example-file path="hr/main/absences/rules.test.yaml" >}}

The fixture says what each person is in `orgs`: roles, teams and team roles, as the server would load them for a request. A delegate whose window has closed is simply a user without that role.

## Around it

- **Onboarding:** an `on_member_joined` [hook](../membership-hooks/) creates the employee's profile, subscribed to their department.
- **Emails in the organization's name:** with [organization context](../organization-context/) and [organization templates](../email-templates/), the "absence requested" and "absence decided" emails carry the organization's brand, and each one edits its own text.
- **The app:** `client.db('main').collection('absences').inTeam(team).create({...})` in the [JavaScript client](../../clients/js/#organizations); the department's managers come from `client.orgs.teams(org).members(team, { role: 'manager' })`.
