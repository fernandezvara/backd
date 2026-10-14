---
title: "Company email templates"
description: "Let each company edit its own emails for the kinds you allow: a logic-less syntax, a sanitized HTML, required placeholders, previews and test sends."
icon: "edit_note"
weight: 526
toc: true
---

With [company context](../company-context/) an email can look like the company's. **Company templates** go one step further: the owners of an organization write the text themselves, for the kinds of email you allow, and backd keeps your own files as the fallback.

```yaml
# realm.yaml
email:
  customizable: [org-invitation, magic-link, reset-password, verify-email, absence-requested]
  overrides:
    permission: manage_org        # who edits: holders of manage: org (the default)
```

Every listed kind must have a template in the realm's [`email/` folder](../../functions/email/#templates), which is what a company's version replaces; startup checks it, and that the realm has organizations. `permission` is `manage_org`, `manage_members`, `manage_roles` or `manage_teams`: the [capability](../../configuration/realm/#organizations) that edits them.

## The syntax

Templates are **logic-less**, in the style of Mustache: owners can't run anything.

```text
Subject:  Reset your {{App.name}} password
Text:     Hi {{User.Email}}, open {{Link}} to choose a new password.
HTML:     {{#App.logo}}<img src="{{App.logo}}" alt="{{App.name}}">{{/App.logo}}
          <p><a href="{{Link}}">Choose a new password</a></p>
          {{^App.logo}}<p>{{Realm}}</p>{{/App.logo}}
```

| Syntax | Means |
|---|---|
| `{{Link}}`, `{{User.Email}}`, `{{App.name}}`, `{{Data.days}}` | a value, **always escaped** (HTML in the HTML part) |
| `{{#App.logo}}…{{/App.logo}}` | a section: shown when the value is there, once per item of a list (`{{.}}` is the item) |
| `{{^App.logo}}…{{/App.logo}}` | an inverted section: shown when it isn't |
| `{{! note }}` | a comment |

The values are `Realm`, `Link`, `ExpiresAt`, `User.Email`, `User.Locale`, `Org.Name`, `Data.*` (what the message carries, like `Data.org_name` or `Data.code`) and `App.*` (what your [data hook](../company-context/#supply-the-data-data-hooks) returned). A value that is missing prints nothing.

There are **no includes, partials, helpers, raw output or delimiter changes**: a tag that asks for one is an error with its line and column. Sections nest at most 6 deep and repeat at most 1,000 times.

## What is checked

- **When it is saved:** each of subject, text and HTML is at most **64 KiB**; the syntax is valid (`400 invalid_template`, with `details` such as `html: line 3, column 5: …`); the **placeholders the kind needs** are there, in the text and the HTML (token kinds need `{{Link}}`, an organization invitation also `{{Data.org_name}}`); and it renders with sample data.
- **When a message is made:** the HTML passes an **allowlist sanitizer**: formatting tags, tables, links (`https` and `mailto`), images (`https`) and a short list of inline styles stay; scripts, styles, forms, frames, event handlers, other tags and comments go. Saving tells you what would be dropped (`removed`).

## The API

| Request | Does |
|---|---|
| `GET /_orgs/{org}/templates` | the kinds that can be customized (with what each must print) and the organization's overrides |
| `GET /_orgs/{org}/templates/{kind}/{locale}` | one template with its versions |
| `PUT /_orgs/{org}/templates/{kind}/{locale}` `{ "subject", "text", "html" }` | save a new version (the last 20 are kept) |
| `DELETE /_orgs/{org}/templates/{kind}/{locale}` | go back to the realm's file |
| `POST …/preview` `{ "subject"?, "text"?, "html"?, "data": "sample" \| "real" }` | render a draft (or the saved version) without sending; `real` calls the data hook |
| `POST …/test` `{ "subject"?, "text"?, "html"? }` | send it to **your own address only**, with `[TEST]` in the subject |

In the JavaScript client: `client.orgs.templates(org).list() / get / put / delete / preview / test`.

- **Preview in a sandbox.** `html` comes already sanitized, but show it in an `<iframe sandbox>` as you would any HTML from users.
- **Previews with real data** call your function, so they are limited to **30 an hour per organization**.
- **Test sends** go through the realm's delivery function to the requesting user's address, at most **10 per hour per organization**, and are audited. They also count against the per-recipient email limits.
- **Everything is audited** (`org.template_save`, `org.template_delete`, `org.template_test`), with who and which kind, never the text.

## When the company's template fails

An email made with an organization context uses the company's template for that kind and language, otherwise your file. If the company's can't be used when the message is made, **the message goes out in your text** and the organization's managers get an `email-template-failed` notice, at most once a day for each kind. Add the folder `email/email-template-failed/` to translate or restyle it.

{{< hint style="warning" >}}
Owners write text your realm sends **from your domain**. The syntax has no way to run code and the HTML is sanitized, but they can still write what they like: let only people you trust edit templates (`overrides.permission`), and keep `customizable` to the kinds that need it.
{{< /hint >}}
