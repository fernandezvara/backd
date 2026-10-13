---
title: "Sign in with a provider"
description: "A minimal sign-in page for testing real Google, Microsoft and Apple providers: redirect sign-in, linking and unlinking."
icon: "key"
weight: 750
toc: true
---

A minimal page with one button per configured [identity provider](../../auth/providers/): *Continue with Google*, *Continue with Microsoft*, *Continue with Apple*. It exists to exercise the provider sign-in flow end to end — sign-up on first sign-in, linking by verified email, linking and unlinking providers on a signed-in account — against real providers, guided by the checklist in `design/identity-providers-testing.md`.

The configuration is the `idtest` realm in `examples/config/idtest`. The application is in `clients/js/examples/providers`.

{{< live-example >}}

## Configure it

Unlike the other examples, this one does nothing until you register OAuth applications in the providers' consoles and fill the placeholders of `examples/config/idtest/realm.yaml`: the file documents, field by field, what each console calls it. Secrets never go in the file; set them on the running server:

```sh
backd login --realm idtest --url "$BACKD_URL" --email <admin email>
printf '%s\n' "$GOOGLE_SECRET"    | backd secret set --realm idtest --name GOOGLE_CLIENT_SECRET
printf '%s\n' "$MICROSOFT_SECRET" | backd secret set --realm idtest --name MICROSOFT_CLIENT_SECRET
backd secret set --realm idtest --name APPLE_SIGN_IN_KEY < AuthKey_<KEYID>.p8
```

The callback to register in each console is `{BACKD_URL}/v1/idtest/_auth/oauth/<provider>/callback`, printed by `backd` at startup. `sign_in.allowed_redirects` must list the origin the app is reached at — `https://localhost:8443` is already there.

{{< hint warning >}}
Apple rejects `localhost` redirect URLs: the web flow needs a public https host. Put the local stack behind a tunnel (`cloudflared tunnel`, `ngrok http 8443`), set `BACKD_URL` to it and add its origin to `sign_in.allowed_redirects`. See [providers](../../auth/providers/#set-it-up) for the details.
{{< /hint >}}

With placeholders left in place the realm still loads — `backd` warns that a provider's secrets are not set — and each button fails at the provider with `provider_unavailable`, so you can wire providers in one at a time.

## Try it

```sh
make example
```

Open <https://localhost:8443/example/providers/> and press a provider button: the browser goes to the provider and comes back to the page, which redeems the one-time code for a session and shows the result — a welcome line on the first sign-in, "Signed in" after that, or a readable message for every `?backd_error=` outcome (`cancelled`, `account_exists`, `link_conflict`, …). Signed in, the page lists the account's sign-in methods (provider, address, verified, when it was linked and last used), removes any but the last, and links another provider to the same account.

The app is plain JavaScript with [Alpine.js](https://alpinejs.dev/) and the [JavaScript client](../../clients/js/), with no build step:

| File | Contents |
|---|---|
| `app.js` | `signInWith` and `auth.ready` for the redirect flow, `linkProvider`/`unlinkProvider`, the `backd_error` messages |
| `index.html`, `style.css` | The page |

`native_client_ids` (Google, Microsoft) and `bundle_ids` (Apple) are only needed for native apps using the [ID-token sign-in](../../auth/providers/#native-apps-the-id-token); this example uses the redirect flow and leaves them empty.
