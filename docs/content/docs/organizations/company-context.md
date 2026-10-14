---
title: "Company context"
description: "Make backd's own emails and hosted pages look like each company's: pass the organization, and an internal function supplies the branding as {{.App}}."
icon: "palette"
weight: 525
toc: true
---

backd's own emails (verification, password reset, magic links, MFA and consent notices, organization invitations) and its [hosted pages](../../functions/email/#links-and-pages) are realm-wide. An app that serves many companies wants them to look like each company's. Two things give them **company context**: the request says which organization the person comes from, and an internal function supplies the data, so backd never reads your collections by configuration.

## Say where the person comes from

Sign-up, sign-in, `POST /_auth/magic-link` and `POST /_auth/reset-password/request` take an optional `org`:

```http
POST /v1/acme/_auth/reset-password/request
{ "email": "ana@example.com", "org": "d3c9ljp8hc2g00b6s1m0" }
```

```js
await backd.auth.requestPasswordReset({ email, org })      // also signIn, login, signup, magicLink.request
```

- **It is a hint, not a fact.** It is used only when the organization exists and the address belongs to one of its members (or has an invitation by email to it). Anything else is ignored **silently**: the same answer and the same work, so `org` can't be used to find out who works where.
- **Invitations carry their organization**, with no `org` needed: an organization's invitation email is always that company's.
- **It sticks to the flow.** The organization is stored with the token (as `redirect_to` is), so the page its link opens, the result page and the emails that follow (a welcome, a "password changed") keep it.
- **From functions:** `ctx.email.send({ kind, to_user, data, org })` takes the same, for the organization the message is about (`400` for one that doesn't exist).

## Supply the data: data hooks

```yaml
# realm.yaml
email:
  data_hook: core/company-data     # <database>/<function>: internal, sync
pages:
  data_hook: core/company-data     # may be the same function
```

Before backd renders a system email or a hosted page it calls the hook with:

```json
{ "kind": "reset-password", "locale": "es", "user": { "id": "…", "email": "ana@example.com" }, "org": "d3c9ljp8hc2g00b6s1m0" }
```

`user` and `org` are there when backd knows them. What the function returns (an object) is the template's `{{.App}}`:

```ts
export default async function (ctx, input) {
  if (!input.org) return {};
  const company = await ctx.admin.db("main").collection("companies").get(input.org);
  return { name: company.name, color: company.color, logo: company.logo_url };
}
```

```gotemplate
<!-- email/reset-password/en.html, pages/reset-password/en.html -->
<h1 style="color: {{if .App}}{{.App.color}}{{else}}#222{{end}}">{{if .App}}{{.App.name}}{{else}}{{.Realm}}{{end}}</h1>
```

- **Guard with `{{if .App}}`.** `.App` is empty when there is no hook, no company or the hook failed. Startup accepts templates that read it.
- **Bounded, and never in the way.** A hook has **2 seconds** and may answer **16 KiB**. On any failure (an error, a timeout, an answer that is too large or isn't an object, a template that can't use what it gave) the message or page renders **without** `App`, and backd logs it: an email is never delayed or dropped by a hook.
- **Startup checks** that each hook is an internal function with `mode: sync`.
- **The delivery function** also receives `org` and `app` in the message's `data`, for one that has templates of its own.

{{< hint style="tip" title="Best practice" >}}
Keep the hook cheap and cache-friendly: it runs for every email and page of a flow, so read one document by id, not a search. Return only what the templates print: names, colours and public URLs, never secrets.
{{< /hint >}}
