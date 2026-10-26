# Shelf — the Dart client demo

A Flutter desktop app (Linux, macOS, Windows) that does what the JavaScript
Shelf demo does, on the tutorial's `shelf` realm — sign-in with the second
factor and magic link, member and curator flows, publishing and public share
links, files, the realm's app configuration, and the live pins view.

It is also `backd types --lang dart` end-to-end: `lib/backd/types.dart` and
`lib/backd/realm.json` are generated from `examples/config/shelf` and every
collection and function call in the app goes through them.

## Run it

```console
$ make dev-examples      # a backd with the shelf realm on :8080
$ cd clients/dart-examples
$ flutter pub get
$ flutter run -d linux   # or macos / windows
```

Sign in with a bootstrap user of the shelf realm, e.g. the one
`backd bootstrap --realm shelf --email you@example.com` made, or the seeded
member/curator accounts the dev stack ships.

## The generated types

```console
$ CONFIG_DIR=examples/config backd types --realm shelf --lang dart --out clients/dart-examples/lib/backd
```

regenerates `lib/backd/types.dart` and `lib/backd/realm.json`; `--check` in
CI fails the build when they drift from the config.

## What it exercises

- **Sign-in:** email + password → `SignInResult` steps (`MfaChallenge` →
  `verifyMfa`, `EnrollmentChallenge` → `mfa.setup`/`confirm` with recovery
  codes, `ConsentChallenge` → `acceptConsents`) and the magic link
  (`magicLink.request` + `verifyCode`).
- **Signing in to a team:** the Team field asks `auth.options(org:)` for the
  methods the team allows, one generic failure message, "Forgot your team?"
  (`auth.findOrgs`), a team switcher in the app bar built from
  `client.orgs.signedIn()` (one session per team, `client.org(ref)`), and
  "Start a team of your own" (`auth.signUpWithOrg`, with the operator's
  invitation).
- **Member flows:** the published gallery (`where`/`orderBy`/`expand`), the
  member's drafts — create/edit/replace with `ifMatch`, upload files, delete —
  and submitting an asset for review.
- **Curator flows:** the review queue, submissions approve/dismiss, and
  `publish` (idempotent).
- **Shares:** `share` mints a token (week/month/forever); `share-open`
  resolves it without an account.
- **Files:** `FileDetails` chips fetch a signed `fileUrl`; `file_picker` +
  `uploadFile` put one on a draft.
- **Pins:** `client.realtime.subscribe` keeps a `LiveView` of the team's pins
  (`where` on `_meta.org`), the `pin` function enforces the plan limit.
- **Config:** `typed.config` typed getters, cached and `refresh: true`.
