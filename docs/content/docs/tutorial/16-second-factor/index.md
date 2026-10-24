---
title: "16. A second factor and magic links"
description: "Sign in with a link by email, require an authenticator app of the admin role, a rule that asks for it, and proofs before sensitive actions."
weight: 360
toc: true
---

Shelf members sign in with a password. This chapter adds two things a real library would want: a way in **without** a password (a link and a code by email), and a **second factor** for the people who can do the most damage, the operators.

It builds on chapter 10's email pipeline: the link arrives in the Mailbox of the Admin view, like every other message.

## Magic links

Add to `config/shelf/realm.yaml`:

```yaml
magic_link:
  enabled: true
sign_in:
  app_url: http://localhost:8080/   # within email.allowed_redirects
```

- **`magic_link.enabled`** turns on `POST /_auth/magic-link`. It needs the `email:` section of chapter 10 and a `BACKD_SECRETS_KEY` on the instance (the tutorial's `compose.yaml` already sets one): `backd` refuses to start without it, because it keeps the address of a request sealed until the email is sent.
- **`sign_in.app_url`** is where the link ends when the app didn't say: the app itself. It must be within the `email.allowed_redirects` of chapter 10.
- **Sign-up stays invite-only.** `magic_link.signup` would let an unknown address make an account by link, and only a realm with `signup: open` allows it. Shelf keeps `signup: invite`: a link signs in members who exist, and a stranger asking for one is told nothing (the answer is the same for every address) and gets a harmless "no account" email.

Restart the stack, **Sign in**, type an address and press **Email me a link**. Open the *Mailbox* in the Admin view as the operator, or read it with the API as in chapter 10: the message has a link and a 6-digit code. Either works, once, for ten minutes:

- **the link**, opened in the same browser, lands on a page of `backd` with a button, and the button sends the browser back to the app, which finishes the sign-in by itself;
- **the code**, typed in the app, works on any device that can read the mail.

The app does it in a few lines:

```js
await backd.auth.magicLink.request(email)          // always answers the same
const outcome = await backd.auth.magicLink.verifyCode(code)   // a session, or a second step
// and, when the link came back to this page:
const outcome = await backd.auth.ready
```

The app you downloaded in the starter is the finished one, so the *Email me a link* form and the code field are already there; they only needed the realm to say yes. (If you made the starter before this chapter existed, fetch `app/app.js`, `app/index.html` and `client/oauth.js` again: the client module the app imports gained `oauth.js` with sign-in by providers.)

## A second factor for the operators

Passwords leak. The operator role can read the audit feed, invite members and make API keys, so give it more than a password. Add this to `config/shelf/realm.yaml` too:

```yaml
mfa:
  required_roles: [admin]
```

Restart `backd`. The next time the operator signs in the password is not enough: since they have no second factor yet, the app shows a QR code and the secret. Scan it with an authenticator app (any TOTP app), type the first code it shows and **Turn it on and sign in**. The *Account* page then shows the **recovery codes**, once: each works one time if the phone is lost. Save them.

From then on a sign-in asks for the code after the password, and `backd` remembers **how** the session was started: `user.mfa` is `true` in rules for it, and `ctx.user.mfa` in functions.

```js
const result = await backd.auth.signIn({ email, password })
if (result.mfaRequired) {
  const session = await backd.auth.verifyMfa(result.challenge, 'totp', code)
} else if (result.enrollmentRequired) {
  const setup = await backd.auth.mfa.setup({ challenge: result.challenge })   // the QR and the secret
  const done = await backd.auth.mfa.confirm({ challenge: result.challenge, setupId: setup.setup_id, code })
}
```

Members can turn it on for themselves on the *Account* page; it is **Two-step sign-in**, next to the password.

{{< hint style="note" >}}
Nothing is revoked when a role starts to require a second factor: sessions that already exist keep working until they expire, and the next sign-in enrols. `backd user mfa-status --realm shelf` lists who hasn't yet.
{{< /hint >}}

## A rule that asks for it

The import log (chapter 11) is only for operators. Make the rule say *and on a session that used the second factor*:

{{< example-file path="shelf/main/imports/collection.yaml" >}}

Restart `backd`. The operator, signed in with a code, still reads the list; a curator gets `403` as before. The point of the extra `&& user.mfa` is a session that can't have it: an API key, or a member session of a person who is also an admin by role but signed in some other way. Rules decide on **how the person got in**, not only on who they are.

## Proving it again before something sensitive

Changing the password or the address, deleting the account, turning the second factor off, or making an API key as a person all ask for a **fresh proof**: `401 reauth_required` until the user shows the strongest thing they have again.

The app asks only when the server wants it:

```js
await backd.auth.withProof(
  (methods) => askTheUser(methods),            // methods: ['totp', 'recovery_code'] or ['password']
  () => backd.auth.deleteAccount(),
)
```

`withProof` makes the call, and on `reauth_required` asks the person, sends the proof to `POST /_auth/reauth` and repeats the call. The proof lives for five minutes (`reauth.ttl` in the realm), so one prompt covers a settings page. See [Re-authentication](../../build/auth/reauth/).

## You should see

- *Sign in → Email me a link* puts a message with a link and a code in the Mailbox; either one signs the member in, and then neither works again.
- The operator's next sign-in shows a QR code. After the first code, the *Account* page lists the recovery codes, and the following sign-ins ask for a code.
- *Account → Two-step sign-in* lets a member turn it on and off; the operator's *Turn it off* is missing (the role requires it).
- *Delete my account* and the email and password changes ask for a code (or the password, without a second factor) before they go.
- The import log answers `200` to the operator and `403` to a curator.

The tour of the tutorial (`make shelf-tour`) checks all of it with the same client library, including the operator's enrolment through a computed TOTP code. Read [Multi-factor authentication](../../build/auth/mfa/) and [Magic links](../../build/auth/magic-links/) for the whole picture: limits, lockouts, break-glass resets and the recovery flow.

Next: [chapter 17](../17-consents/) — terms and consents.
