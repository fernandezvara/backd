---
title: "The sign-in experience"
description: "What a person sees when they sign in to an organization: the company from the address or one field, the methods it offers, one failure message, a switcher for several, and how to start, join and find a company. With code for JavaScript and Dart."
icon: "sign-in"
weight: 519
toc: true
---

An organization's people sign in *to* it, so a sign-in page has one question more than usual: which company? How the page asks decides whether signing in feels natural or like a chore. These are the flows and the rules the Shelf example follows.

## The flows

| Person | What they see |
|---|---|
| Returning, the company is in the address (`acme.app.com`, `/acme`) | The methods that company allows and nothing else to type |
| Returning, one sign-in page | **Company** (filled in with the last one used on this device, with a "not yours?" link), then the methods |
| The company requires its own provider (SSO) | Only "Continue with Microsoft", no password form that would fail |
| First time, starting a company | Email, password, **company name**; the short name (`acme-corp`) is suggested from the name and can be edited. One call makes the account, the company and the session |
| Invited | The link already names the company: the page says "Join Acme as editor?" and offers sign-in or sign-up, no company field |
| On a new device, forgot the company | "Forgot your company?" under the field emails the address its companies, with the links |
| Several companies on this device | A switcher built from the sessions this browser holds; picking one is instant |

## The page

```js
const backd = createClient({ url, realm, org: companyFromAddressOrField })   // an id or the slug people type
const { providers, methods } = await backd.auth.options()    // ['google'], ['password', 'magic_link']
await backd.auth.login({ email, password })                  // or magicLink.request, or signInWith(provider)
```

```dart
final backd = BackdClient(url: url, realm: realm, org: companyFromAddressOrField);
final (:providers, :methods) = await backd.auth.options();
await backd.auth.login(email, password);
```

Show the methods **after** the company, never before: they depend on it. A company that requires its provider answers `providers` with only its buttons and rejects the others with `sso_required`.

## Rules for the page

- **Ask for the company only when the address does not say it,** as one short field that accepts any case and surrounding spaces (`" Acme "` is `acme`). Prefill it with the last company used on this device.
- **Say the same thing for every failure:** "We couldn't sign you in. Check the company, email and password." with "Forgot your company?" and "Forgot your password?" next to it. The page cannot tell which was wrong, and neither can the API: a wrong password, a person who is not a member and a company that does not exist all answer `invalid_credentials`.
- **Remember, don't list.** The browser keeps the companies it signed in to (`backd.orgs.signedIn()`, with their names once `auth.org()` was asked); the API never lists the companies of a person. That is also why "Forgot your company?" sends an email: the answer goes to the address's mailbox, and the caller learns nothing.
- **Come back to where the person was** after a redirect to a provider or a magic link: the redirect carries the company (`backd_org`) and the client keeps the session under it.
- **Sign out to the company's own page,** with the company still filled in.

## Starting, joining, finding

- **Start a company:** `auth.signUpWithOrg({ email, password, name, slug })`. In an invite-only realm the person also gives the operator's `invitation`. The `slug` is unique (`slug_taken` makes nothing); suggest one from the name.
- **Join with an invitation:** `auth.previewInvitation(token, { org })` says what the link offers, before anyone signs in. Then `auth.login({ email, password, invitation })` for someone with an account, or `auth.signup({ email, password, invitation })` for someone without; the invitation is used up as part of that.
- **Find the company:** `auth.findOrgs({ email })` always resolves; an address that belongs to companies gets an email listing each one's sign-in page. The realm needs `organizations.signin_link` and `email:`.
- **Work in several:** `backd.org('beta')` is the client of another company, with a token of its own; `backd.orgs.create(...)` opens the session of the company it makes. See [Working in an organization](../../clients/js/#working-in-an-organization).

The [Shelf app](../../../tutorial/20-teams/) is a complete page built this way.
