---
title: "17. Terms and consents"
description: "Terms members accept to have an account, an optional consent for the digest by email, a function that respects it, and what happens when the terms change."
weight: 370
toc: true
---

A team library holds people's names and what they publish. Before anyone has an account, Shelf should say **what they are agreeing to** and keep proof of it. This chapter adds two consents: the **terms** (needed for an account) and the **digest by email** (optional), and makes the digest respect a member who says no.

{{< hint style="note" >}}
`backd` provides the mechanisms: versioned texts, a record of each acceptance, withdrawal, and gating. Which texts you need and why is for you and your lawyers; nothing here is legal advice.
{{< /hint >}}

## Declare the consents

Add to `config/shelf/realm.yaml`:

```yaml
consents:
  terms:
    required: true
    current: "2026-10"
  marketing:
    required: false
    current: "1"
```

The texts are files, one per version and language of the realm (Shelf has English only):

```
config/shelf/_realm/consents/terms/2026-10/en.md
config/shelf/_realm/consents/marketing/1/en.md
```

```markdown
# Shelf terms of use

Shelf is a team library. By creating an account you agree to:

- keep your credentials to yourself;
- only publish what your team may share;
- let the operators remove content that breaks these rules.
```

Write the text you need there; the digest's own text (`marketing/1/en.md`) is a sentence saying what the member opts in to and that they can withdraw it.

`backd` hashes each text (SHA-256) and includes the files in the config fingerprint. Keep every version you publish in the folder (and in git): each record names the hash of the words a member accepted. Restart `backd`; it refuses to start if the current version has no text.

## Sign-up and sign-in

The texts are public, so the sign-up form can show them before an account exists:

```js
const list = await backd.consents.current()                 // [{ name, required, current: { version }, locales }]
const terms = await backd.consents.text('terms', '2026-10') // { text, hash, … }

await backd.auth.signup({ email, password, invitation, consents: { terms: '2026-10', marketing: '1' } })
```

Without the terms, sign-up is `400 consent_required` and no account is made (the invitation is not used up). The sign-up form of the app shows each text under a checkbox, and the terms' box is required.

What about people who already have an account? The next time **anyone who never accepted the terms** signs in, the answer is not a session but a step, like the second factor of chapter 16:

```js
const result = await backd.auth.signIn({ email, password })
if (result.consentRequired) {
  // show the texts of result.consents, then:
  const session = await backd.auth.acceptConsents(result.challenge, { terms: '2026-10' })
}
```

The step comes **after** the password and any second factor, so the operator's first sign-in on a fresh stack sets up an authenticator app and *then* accepts the terms. The app shows the consent step with the texts and an **Accept and sign in** button.

## The digest respects a no

Members opt in to the digest by email with the second checkbox, or later on the *Account* page, where each optional consent is a switch. Chapter 10's digest function sends a mail to every member; now it says whose consent it needs:

{{< example-file path="shelf/main/_functions/digest/index.ts" lines="31-40" >}}

`requires_consent: "marketing"` makes `backd` skip a member who doesn't hold the consent (they never gave it, or withdrew it): nothing is queued, and the send answers `{ status: "no_consent" }`. The in-app notification still goes to everyone, since it is not marketing. The check belongs to `backd`, not to the function's code, so a withdrawal is respected even when someone later edits the function and forgets it.

A member who switches the digest off on the *Account* page stops receiving it at once:

```js
await backd.consents.withdraw('marketing')    // an optional consent; the terms answer 409
await backd.consents.accept({ marketing: '1' })
```

The unit test of the function plays it with `createContext({ email: { consents: { u1: ['marketing'] } } })`: only `u1` is mailed.

## When the terms change

Publish a new version by adding its folder and moving `current`:

```yaml
consents:
  terms:
    required: true
    current: "2026-11"
    versions:
      "2026-11": { reaccept: true, reaccept_by: 2026-12-15 }
```

- **`reaccept: true`** is a substantial change: people who accepted an earlier version are asked again at their **next sign-in**, with the same consent step. A correction that doesn't need it (`reaccept: false`, or leaving it out) just becomes current.
- **`reaccept_by`** is the date after which open sessions of people who haven't accepted are refused with `403 consent_required`, until they accept with `backd.consents.accept(...)`. Without it, sessions continue until the member signs in again.
- **Telling them** is a decision, not a side effect: `backd consents notify --realm shelf --consent terms` emails the members who hold an older version.

The Account page's consent list shows `accepted: version …` for each, from `GET /_auth/consents`.

## You should see

- The sign-up form shows the terms and the digest text, and refuses to go on without the terms.
- Signing in with an account from before this chapter shows the consent step once; the next sign-in goes straight through.
- *Account → Consents* has the digest switch; with it off, running the `digest` function from the Admin view makes notifications but no mail.
- Withdrawing the terms is refused (they are what the account needs).

The tour of the tutorial (`make shelf-tour`) checks it: a sign-up without the terms is `consent_required`, the member holds both consents with their history, the terms can't be withdrawn, and no digest mail follows a withdrawal. Read [Consents](../../build/auth/consents/) for the whole picture: records, re-acceptance, rules (`hasConsent`), erasure and retention.

Next: [chapter 18](../18-app-config/) — app configuration.
