# Design: sign-in with external identity providers (realm and organizations)

- **Issues:** the built part: #141–#146, #214–#216 (realm providers), milestone 30 (organization connections), milestone 34 (scoped sessions). New: generic OpenID Connect for the realm and for organizations (#157, #599), GitHub and generic OAuth 2 (audit B6), verified domains (#601), the `sso/` library (§3). Closed by this design: #598 (Apple for organizations, §2). Not created yet: the milestone is named when the issues are written (§14).
- **Status:** decided 2026-11-15 (the questions of §2 were answered one by one). **Replaces** `identity-providers.md` and `org-sso.md`, and absorbs `verified-domains.md`; §16 says where each old section went.
- **Testing with real providers:** [identity-providers-testing.md](./identity-providers-testing.md)
- **Related designs:** [scim.md](./scim.md) (provisioning; depends on §10 here), [scoped-sessions.md](./scoped-sessions.md) (sessions scoped to an organization, `sso_required`), [auth-strategies.md](./auth-strategies.md) (`sso` and provider names as strategy methods), [organizations.md](./organizations.md) (members, roles, capabilities), [mfa-passwordless.md](./mfa-passwordless.md) (login codes, second steps), [account-lifecycle.md](./account-lifecycle.md) (verification, erasure, sign-up modes), [email-delivery.md](./email-delivery.md) (hosted pages, `BACKD_URL`), [internal-functions.md](./internal-functions.md) (`account.on_signup`), [sign-in-audit.md](./sign-in-audit.md)

## 1. The idea

A person signs in to backd with an account at someone else: Google, Microsoft, Apple, GitHub, the company's own OpenID Connect server. backd checks who they are, finds or creates the matching user, and hands the app a session. Two places can own such a provider:

- **The realm** (the operator): "Continue with Google" for everyone who uses the application. Configured in `realm.yaml`.
- **An organization** (a company that uses the application): its own Google Workspace, Microsoft Entra tenant or OpenID Connect server, with its own secret, restricted to its own people, configured by its own managers without the operator.

Both are the same machinery with different inputs, and the design keeps them as one: one library (§3), one provider model (§4), one flow (§7), one set of account rules (§8). What differs is *who may configure it* and *what proves a person belongs there* (§9).

Standing rules (unchanged from the designs this replaces):

- **A session token never appears in a URL.** A browser flow ends at the app with a one-time login code that the app redeems with `POST` (§7).
- **Users hold authentication data only;** profiles live in app collections. backd stores no provider access or refresh tokens, except Apple's refresh token, encrypted, for revocation (§7.5).
- **Nothing says whether a company, an account or an address exists** where it can be hidden.
- **A provider an organization controls can say anything about the people it authenticates,** so it is trusted only for what can be proved independently of it (§9).

## 2. Decisions at a glance

Decisions marked ★ were taken in the design interview of 2026-11-15; the rest are carried over from the designs this one replaces.

| Topic | Decision |
|---|---|
| ★ Code | A standalone library `sso/` (a public package of this module, import-guarded) holds providers, the protocol and the whole start/callback/link/join decision logic. backd plugs in through four ports. The HTTP handlers only parse, call the engine and render (§3) |
| Provider kinds | Built in: `google`, `microsoft`, `apple`, `github`. Generic: `type: oidc` and `type: oauth2` (§4) |
| ★ Where each kind is allowed | Realm: all kinds. Organizations: `google`, `microsoft` and `oidc` only. Apple, GitHub and `oauth2` are realm only: no company proof for an access-token flow, no `hd`/`tid` equivalent for Apple, and a tenant must not point backd at arbitrary API endpoints (§4.1) |
| ★ Issuer control for organizations | A realm allow-list of issuer patterns (`organizations.sso.oidc.issuers`); empty means organizations cannot use OIDC. https only, same-registered-domain discovery, and no private, loopback, link-local or metadata address, whatever the list says (§5.2, §11.1) |
| ★ What admits a person through an organization's OIDC | A **verified domain** of the organization (§10): the token's email must be verified and on one of its domains. `google` keeps `hd`, `microsoft` keeps `tid` (§9.2) |
| ★ Identity key | `(provider, org, issuer, subject)`. Every identity records the issuer that vouched for its subject; a changed issuer matches nothing instead of matching someone else (§6.1) |
| ★ GitHub and OAuth 2 | GitHub links by email automatically under the Google rules (primary address that GitHub marks verified); `oauth2` and `oidc` providers never do unless the operator sets `link_by_email: true` (and, for `oauth2`, the userinfo carries an explicit verified flag) (§8.1) |
| ★ Private identity providers | Operator-written providers may set `allow_private_network: true` to reach their own hosts on private ranges (never loopback, link-local or metadata). Organizations can never (§11.1) |
| ★ Apple for organizations | **Not built** (#598 closes as won't-do): Apple gives no company proof and its relay addresses are never on a verified domain |
| ★ Documents | This file replaces `identity-providers.md` and `org-sso.md` and absorbs `verified-domains.md`; `scim.md` stays apart and depends on §10 (§16) |
| Flows | Redirect flow run by backd and an ID-token endpoint for native apps (OIDC kinds and Apple); the redirect flow only for `github` and `oauth2` (§7) |
| Getting a session | One-time login code (1 min, single use, hashed, bound to the app's PKCE challenge) redeemed at `POST /_auth/token` |
| Linking by email | Realm: only when the provider asserts a verified address, is trusted for it (Google, Apple, GitHub) and the backd user is verified, on first sight only. Organizations: only through a verified domain (§8.1) |
| Configuration | Public settings under `auth.providers` in `realm.yaml`; secrets in the encrypted store (`secret:NAME`; organization secrets in their own scope, never readable) |
| Network | backd's own restricted HTTP client; hosts are the provider's, never `backd egress` (§11.1) |
| Organization sign-up | An organization sign-in never links an existing account by email except through a verified domain; it creates users even when the realm's sign-up is `closed`, because the realm switch is the operator's consent (§8) |

## 3. The library

### 3.1 Why and where

Today sign-in with providers is spread over three layers that know each other: `internal/oauth` (protocol, but it takes backd's `registry.Provider`), `internal/auth/oauth.go`, `callback.go` and `orgsso.go` (the account rules, mixed with Mongo and sessions), and `internal/httpapi/oauth.go`, `oauth_native.go` and `orgsso.go` (about 1,200 lines that do the real work of a request). Adding OIDC and OAuth 2 to that shape would add a fourth special case to each layer.

`sso/` is a **public package of this module** (`github.com/fernandezvara/backd/sso`) that knows neither backd, nor MongoDB, nor organizations, nor sessions. A test (`go list -deps`) fails if it imports anything under `github.com/fernandezvara/backd`, the same guard `scim/` gets. It can move to its own module, as `kli` did, with no change to the code.

```
sso/
├── (root package sso)        Provider, Kind, Scope, Claims, Attempt, Engine, errors, and the four ports
│     engine_start.go         begin a sign-in or a link
│     engine_callback.go      finish it: exchange, verify, decide (§8), issue the login code
│     engine_native.go        the ID-token endpoint
│     engine_redeem.go        the login code → who signed in and how
│     decide.go               the account decision table (§8.1) as one pure function
├── oidc/                     discovery, JWKS cache, ID-token verification, claim rules (§11.2)
├── oauth2/                   authorize URL, code exchange, userinfo, per-kind claim mapping (github, generic)
├── apple/                    the client-secret JWT, the form_post callback, revocation
├── safehttp/                 the restricted HTTP client: host allow-list, public-address dialer, size and time caps
├── pkce/                     verifier, S256 challenge, validation
├── httpsso/                  net/http handlers over the Engine (start, callback, id-token, token, providers)
└── ssotest/                  fake OIDC and OAuth 2 provider (today's oauthtest), fake ports, Store conformance suite

backd side
├── internal/mongodb/sso.go            Store over oauth_states and login_codes
├── internal/auth/sso_catalog.go       Catalog: realm.yaml providers and organization connections → sso.Provider
├── internal/auth/sso_directory.go     Directory: identities, users, sign-up mode, joining, blocked, sessions' org
├── internal/auth/sso_events.go        Events: audit, jobs (Apple revocation, account.on_signup)
├── internal/auth/orgsso.go            the connection model and its management (§9)
├── internal/auth/orgdomains.go        verified domains (§10)
├── internal/registry/providers.go     the realm.yaml sections; the one validator both places use
└── internal/httpapi/sso.go            mounts httpsso, the session and rate-limit hooks; the management API
```

### 3.2 What the library speaks

| Type | Meaning |
|---|---|
| `Scope` | An opaque string chosen by the host (`realm`, `org:<id>`); the library never parses it. It travels in the attempt, so a code started in one scope cannot finish in another |
| `Provider` | A resolved, ready-to-use provider: `Name`, `Kind`, `Issuer`, endpoints, `ClientID`, the client secret **value** (supplied by the Catalog, never stored by the library), audiences, `Restrictions` (§9.2), `Link` policy (`never`, `verified_email`, `verified_domain`), `Domains` (verified domains, for `verified_domain`), `AllowedHosts`, `AllowPrivate`, `Scope` |
| `Claims` | What a verified token or userinfo gave: `Issuer`, `Subject`, `Email`, `EmailVerified`, `Profile`, and the kind's extras (`HostedDomain`, `TenantID`, `PreferredUsername`) |
| `Attempt` | The stored state of one browser flow: provider, scope, intent (`signin` or `link`), the app's PKCE challenge, backd's own PKCE verifier and nonce, `redirect_to`, the invitation, the linking user |
| `Outcome` | What the engine tells the caller: `LoginCode{code, redirect}`, `Linked{provider, redirect}`, `Session{...}` (native), or a typed `Refusal` with one of the codes of §12.2 |

### 3.3 The four ports

```go
// Store keeps what the library creates and must find again. One-time reads are atomic.
type Store interface {
    PutAttempt(ctx, hash string, a Attempt, ttl time.Duration) error
    TakeAttempt(ctx, hash string) (Attempt, error)            // single use: an atomic delete
    PutCode(ctx, hash string, c Code, ttl time.Duration) error
    TakeCode(ctx, hash string) (Code, error)                  // single use
}

// Catalog says which providers a place offers right now and holds their secrets.
type Catalog interface {
    Providers(ctx, Scope) ([]Provider, error)                 // only usable ones: configured, enabled, secret set, scope active
    Provider(ctx, Scope, name string) (Provider, error)       // ErrUnavailable for every reason, never saying which
}

// Directory is backd's accounts, as far as sign-in is concerned.
type Directory interface {
    Identity(ctx, IdentityKey) (Account, error)               // ErrNotFound
    UserByEmail(ctx, email string) (Account, error)           // carries Verified; ErrNotFound
    Create(ctx, NewAccount) (Account, error)                  // honors sign-up mode, invitation, joining (§8.2)
    Link(ctx, Account, IdentityRecord) error                  // ErrConflict: taken, or the user already has one here
    Touch(ctx, IdentityKey, Claims) error
    Admit(ctx, Account, Scope) error                          // disabled, networks, verification, blocked, org active
}

// Events is everything that happens because a sign-in happened.
type Events interface {
    Audit(ctx, Event)
    NewUser(ctx, Account, Profile)                            // account.on_signup, the one-time profile
    AppleToken(ctx, Account, encryptedRefreshToken []byte)    // stored for revocation
}
```

**What decides where a rule lives**

- **In the library:** every rule that is about identity protocols or the sign-in decision. The kinds and their quirks, discovery, token and signature checks, issuer, audience, nonce, PKCE, state, the safe HTTP client, the restriction checks of §9.2, the decision table of §8.1 (a pure function over `Claims`, `Provider`, what the Directory found and the attempt), the login code, error codes.
- **In backd, through the ports:** what an account *is*. Users, identities in MongoDB, sign-up mode, invitations, organization membership, `join_roles`, `blocked`, sessions, MFA steps after redemption, roles, audit records, secrets and the realm and organization configuration.
- **Never in the library:** a Mongo type, an organization role, a session, a secret's storage, a route table of backd.

### 3.4 How thin the handlers become

A handler is about ten lines: resolve the scope from the path, read the inputs, call one `Engine` method, write the `Outcome`.

```go
func (h *handlers) callback(w http.ResponseWriter, r *http.Request) {
    in := sso.CallbackInput{State: param(r, "state"), Code: param(r, "code"), ProviderError: param(r, "error")}
    out := h.engine.Callback(r.Context(), in)   // never returns an error: failures are an Outcome
    httpsso.Redirect(w, r, out)                 // 302 to redirect_to with backd_code or backd_error, or the hosted page
}
```

The same `httpsso` handlers serve the realm (`/_auth/oauth/{provider}/…`) and an organization (`/_auth/orgs/{org}/oauth/{provider}/…`). The only difference is the `Scope` the backd route puts in the context, so there is a single copy of each handler and of its tests. The management routes (§9.3) stay in `internal/httpapi`, because they are about backd's organizations.

### 3.5 Tests

The library is tested alone with `ssotest`: a fake OIDC provider (discovery, JWKS, rotation, bad signatures, foreign hosts, replays), a fake OAuth 2 provider (GitHub-shaped and generic), fake ports and a **conformance suite** (`ssotest.TestStore`, `TestDirectory`) that backd's Mongo-backed implementations must pass. The decision table has a golden test with every row of §8.1. The backd side is tested against real MongoDB and the contract tests.

## 4. Providers

A provider is a **kind** plus configuration. The kind says how to run the flow, what the subject and the email are, and how far the email can be trusted.

| Kind | Protocol | Subject | Email | Trusted to link | Native apps |
|---|---|---|---|---|---|
| `google` | OIDC (fixed issuer) | `sub` | `email`, `email_verified`; `hd` for Workspace | yes | yes (`native_client_ids`) |
| `microsoft` | OIDC (issuer per tenant) | `tid` + `oid` | `email`, accepted on the domain of `preferred_username` (§11.2) | no | yes |
| `apple` | OIDC, `form_post` | `sub` | `email`; relay addresses count as verified | yes | yes (`bundle_ids`) |
| `github` | OAuth 2 (no ID token) | the numeric `id` (never the `login`, which can be renamed and reused) | the primary address GitHub marks verified, from `/user/emails` | yes | no |
| `oidc` | OIDC, discovery from `issuer` | `sub` (with the issuer, §6.1) | `email` + `email_verified` | only with `link_by_email` | no |
| `oauth2` | OAuth 2 with configured endpoints | the field `claims.subject` of the userinfo | `claims.email`, verified only if `claims.email_verified` is mapped and true | only with `link_by_email` and a mapped verified flag | no |

### 4.1 Who may use which kind

| Kind | Realm (`realm.yaml`) | Organization connection |
|---|---|---|
| `google`, `microsoft` | yes | yes (restricted by `hd` / `tid`) |
| `oidc` | yes, any issuer the operator writes | yes, **only** an issuer matching the realm's allow-list, and only with a verified domain (§9.2) |
| `apple` | yes | **no** |
| `github`, `oauth2` | yes | **no** |

Why the three are realm only. Apple has no company identifier, and Sign in with Apple's private-relay addresses are never on a domain an organization can verify, so the one proof organizations rely on does not exist. An access-token flow (GitHub, plain OAuth 2) has no signed token that names a company and would let a tenant make backd call API endpoints it chooses. They can be added later with their own proof (for GitHub, membership of a named GitHub organization), designed then.

### 4.2 GitHub and generic OAuth 2

There is no ID token, no nonce and no signature: the access token returned by the token endpoint is the only proof, so everything is fetched over TLS from the configured hosts, once, and the token is discarded.

- **State and PKCE** protect the callback as in the OIDC flow. `oauth2` providers declare whether the server supports PKCE (`pkce: true`, default true for `github`).
- **GitHub:** scopes `read:user user:email` only; endpoints on `github.com` and `api.github.com`; `GET /user` for the numeric `id` and the profile, `GET /user/emails` and the entry with `primary` and `verified` both true. If there is none the sign-in ends with `email_required`.
- **`type: oauth2`** names `authorize_url`, `token_url`, `userinfo_url` and the `claims` map from userinfo fields to the subject, email, verified flag and profile. The hosts of the three URLs are the allowed hosts. A GitHub Enterprise server is an `oauth2` provider.
- **The subject must be a stable, never reused identifier** (an id, not a handle). The startup check refuses a `claims.subject` named `login`, `username`, `preferred_username` or `email`.
- Responses are size-capped, must be JSON, and the token is never logged, stored or returned.

## 5. Configuration

### 5.1 Realm providers (`realm.yaml`)

The sign-in keys live under `auth:` ([auth-strategies.md](./auth-strategies.md)). A provider named in `auth.strategies` must be configured here, or the realm does not start.

```yaml
auth:
  sign_in:
    allowed_redirects: [https://app.acme.example, "acme://"]
    app_url: https://app.acme.example      # where a sign-in without redirect_to goes back to
  providers:
    google:
      client_id: 1234.apps.googleusercontent.com
      client_secret: secret:GOOGLE_CLIENT_SECRET
      native_client_ids: [5678.apps.googleusercontent.com]
    microsoft:
      client_id: 00000000-0000-0000-0000-000000000000
      client_secret: secret:MICROSOFT_CLIENT_SECRET
      tenant: common                       # common | organizations | consumers | <tenant id>
    apple:
      services_id: com.acme.web
      bundle_ids: [com.acme.ios]
      team_id: ABCDE12345
      key_id: XYZ987
      private_key: secret:APPLE_SIGN_IN_KEY
      revoke_on_delete: true
    github:
      client_id: Iv1.0123456789abcdef
      client_secret: secret:GITHUB_CLIENT_SECRET
    keycloak:                              # any other name is a generic provider
      type: oidc
      issuer: https://sso.acme.example/realms/acme
      client_id: backd
      client_secret: secret:KEYCLOAK_CLIENT_SECRET
      scopes: [groups]                     # added to openid email profile
      link_by_email: false                 # default false
      allow_private_network: false         # default false, see §11.1
    partner:
      type: oauth2
      issuer: https://partner.example     # an identifier you choose, stored on every identity (§6.1)
      authorize_url: https://partner.example/oauth/authorize
      token_url: https://partner.example/oauth/token
      userinfo_url: https://partner.example/api/me
      client_id: backd
      client_secret: secret:PARTNER_CLIENT_SECRET
      scopes: [profile, email]
      claims: { subject: id, email: email, email_verified: email_verified, name: name, picture: avatar }
      pkce: true
      link_by_email: false
```

- **Callback URL to register:** `{BACKD_URL}/v1/{realm}/_auth/oauth/{provider}/callback` (or `email.public_url` when the realm sets it). `backd template realm` writes the sections commented, with where to create each app and the URL to register.
- **Names:** `google`, `microsoft`, `apple` and `github` are reserved and imply their kind; `password`, `email`, `sso`, `oidc` and `oauth2` are reserved too. Any other name must declare `type: oidc` or `type: oauth2` and match `[a-z][a-z0-9-]{0,31}`. The name is what `identities.provider`, the URLs and the audit use; renaming a provider orphans its identities.
- **Scopes are fixed:** `openid email profile` (Apple `name email` with `response_mode=form_post`; GitHub as in §4.2). backd never asks for access to a provider's APIs and never for `offline_access`; generic providers may list extra `scopes:` their issuer needs.
- **Redirect targets** come from `auth.sign_in.allowed_redirects`, falling back to `email.allowed_redirects`; with neither, `start` refuses every `redirect_to`.
- **Startup checks:** strict parsing, required keys, well-formed identifiers, valid and unique names, https URLs (http only under `BACKD_DEV`), no `claims.subject` of a mutable name, and for `oidc` that the discovery document, when it is fetched, names the configured issuer. A referenced secret that is not set is a startup warning and that provider answers `503 provider_unavailable`.
- Secrets are realm secrets managed with `backd secret set --realm <r> --name NAME` (encrypted, admin only, audited); rotation needs no restart. backd generates Apple's client-secret JWT (ES256) and renews it.

### 5.2 What the realm lets organizations do

```yaml
organizations:
  roles:
    owner: { manage: [org, members, roles, teams, sso, scim, domains] }
  sso:
    enabled: true
    providers: [google, microsoft, oidc]   # default: google, microsoft
    max_per_org: 3
    oidc:
      issuers:                             # empty or absent: organizations cannot use oidc
        - https://*.okta.com
        - https://login.microsoftonline.com
        - https://sso.partner.example
  domains:                                 # §10
    enabled: true
    max_per_org: 10
    recheck: 7d
    lapse_after: 3
    blocked: [example-free.test]
    resolvers: []
```

- With `organizations.sso` off, the whole surface answers `404`.
- **Issuer patterns** are an https origin, optionally with `*.` as the first host label (`https://*.okta.com`: one label, so `https://evil.okta.com.attacker.example` and `https://a.b.okta.com` do not match). A pattern has no path, no port other than 443 and no credentials. The issuer of a connection must match a pattern, its discovery host must match the same pattern's registered domain (§11.1), and the match is made again at every discovery refetch, so removing a pattern stops its connections at once.
- The load check refuses a wildcard on a public suffix (`https://*.com`).

## 6. Data

### 6.1 `identities`

One document per sign-in method of a user.

| Field | Notes |
|---|---|
| `user_id`, `provider`, `org` | `provider` is the realm provider's name, or `google`, `microsoft`, `oidc` for an organization's connection; `org` is empty for realm providers |
| `issuer` | **new.** The exact issuer that vouched for `subject`: Google's, Apple's, `https://login.microsoftonline.com/<tid>/v2.0`, `https://github.com`, a generic provider's issuer. For `oauth2` the configured `issuer` identifier |
| `subject` | the stable id from §4 |
| `email`, `email_verified` | as the provider gave it, updated at every sign-in; never changes `users.email` |
| `created_at`, `last_used_at` | |
| `apple_refresh_token` | Apple only; encrypted; never returned or logged; used only for revocation |

- **Indexes:** unique on `(provider, org, issuer, subject)` and on `(user_id, provider, org)`. A user holds one identity per provider per organization; a person who needs two Microsoft tenants in the realm uses two backd accounts.
- **Sign-in matches the whole key.** If an operator or a manager changes an issuer, the old identities stay in the table but **match nothing**: a different person who happens to have the same `sub` at the new issuer is a stranger, not the old user. A signed-in user who links the connection again (`intent=link`) replaces their own dormant identity of that connection; no sign-in path ever re-binds one.
- **Changing an issuer** shows the manager or the operator how many identities become dormant, and an organization's change goes through every check of §9 again.
- **Password** identities are unchanged (`provider: password`, `subject` the user id, no issuer).
- **Rollout.** `backd provision` adds the field and the indexes. Existing built-in identities get their `issuer` in the same run (Google's and Apple's are fixed; a Microsoft one is built from the `tid` in its subject), so no sign-in breaks. `PROVISION_MODE=verify` refuses to start until it has run. This is the one breaking step of the redesign (§14).

**Not stored:** provider access tokens, refresh tokens (other than Apple's), ID tokens, userinfo bodies, profile data.

### 6.2 Attempts and codes

- **`oauth_states`** (realm system database): keyed by the SHA-256 of the `state`, TTL 10 minutes, single use (an atomic delete at the callback), holding the provider, `scope`, intent, the app's PKCE challenge, backd's PKCE verifier and nonce for the provider, `redirect_to`, the invitation token hash and the linking user id. Created by `backd provision`, so several instances work without sticky routing.
- **`login_codes`**: keyed by the SHA-256 of the code, TTL 1 minute, single use, holding the user id, the first factor (`method`: the provider's name, which lets a second step follow the redemption), the app's PKCE challenge, `redirect_to`, `via_org` and the one-time profile for new users. The code is not specific to providers: magic links and passkeys end the same way ([mfa-passwordless.md §7.4](./mfa-passwordless.md)).

### 6.3 Organization data

`org_connections`, `org_domains` and organization secrets are in §9 and §10.

## 7. Flows

### 7.1 The redirect flow (every kind)

```mermaid
sequenceDiagram
    participant App as App (browser or mobile)
    participant B as backd (sso engine)
    participant P as Provider
    App->>B: POST /_auth/oauth/{provider}/start { redirect_to, code_challenge, intent?, invitation? }
    B->>B: scope + provider from the Catalog; redirect_to ∈ allowed_redirects; store the attempt (state, nonce, own PKCE)
    B-->>App: { authorize_url }  (GET answers 302 for plain sign-in)
    App->>P: user signs in and consents
    P-->>B: GET/POST /_auth/oauth/{provider}/callback (code, state)
    B->>B: take the attempt by hash(state); scope and provider must match
    B->>P: exchange the code (client secret / Apple JWT) with PKCE
    P-->>B: id_token (OIDC kinds) or access_token (github, oauth2)
    B->>B: OIDC: verify the ID token (§11.2). OAuth 2: fetch userinfo and map the claims
    B->>B: restrictions of the provider (§9.2), then the decision table (§8.1)
    B-->>App: 302 redirect_to?backd_code=<one-time login code>   (or ?backd_error=<code>)
    App->>B: POST /_auth/token { code, code_verifier }
    B-->>App: session (+ new_user, profile), or the second-step challenge
```

- `start` takes `redirect_to` (optional when the realm sets `auth.sign_in.app_url`; neither is `400 invalid_redirect`) and requires a PKCE `code_challenge` (S256 only).
- **Two forms of `start`:** `GET` answers `302` for a plain sign-in; `POST` answers `{authorize_url}` and is the only form for `intent: link`, which needs the session in `Authorization`. The JS client uses `POST` for both.
- The callback accepts **GET and POST** (Apple posts a form).
- The code is bound to the app's PKCE challenge: a stolen code is useless without the verifier.
- The parameters backd adds to `redirect_to` are prefixed (`backd_code`, `backd_error`, `backd_provider`, `backd_linked`).
- **Organization sign-in** is the same flow at `/v1/{realm}/_auth/orgs/{org}/oauth/{provider}/…`. The scope is stored in the attempt and must equal the path's at the callback, so an attempt started for one organization (or the realm) cannot be finished at another.
- Existing account rules apply at the end: a disabled user or a `login_networks` mismatch ends with `backd_error=signin_refused` (one code; the audit has the reason), and `require_verified_email` with `email_not_verified`.

### 7.2 ID-token endpoint (native apps)

`POST /_auth/oauth/{provider}/id-token { id_token, nonce, authorization_code? }`, and the same under an organization.

- For platform sign-in (Sign in with Apple on iOS, Google Sign-In, MSAL). backd verifies signature, issuer, audience (`client_id` plus `native_client_ids` / `bundle_ids`), expiry and the nonce. The app sends the raw nonce; backd accepts a token whose `nonce` equals it **or its SHA-256**. The nonce is always required.
- Only OIDC kinds with native audiences have it (`google`, `microsoft`, `apple`, and an organization's `google` / `microsoft` connection with its `native_client_ids`). `github`, `oauth2` and generic `oidc` providers answer `404`.
- Apple: `authorization_code` is required when `revoke_on_delete` is true (§7.5).
- The same decision table as the redirect flow; outcomes are JSON: a session (with `new_user`, `profile`), or `409 account_exists`, `403 signup_closed`, `403 email_not_verified`, `400 invitation_invalid`, `403 not_allowed`, `401 invalid_token`.

### 7.3 Linking

`intent: link` needs a session and uses `POST …/start`. The flow ends at `redirect_to?backd_linked=<provider>` (no login code). The identity is attached to the signed-in user if it is free and the user has none for this provider (and organization); else `link_conflict`.

### 7.4 Errors and the hosted page

The callback sends failures back to `redirect_to` as `?backd_error=<code>` (§12.2). Nothing sensitive is included; the JS client maps them to `BackdError`s. **One hosted page, `oauth-error`,** covers attempts that cannot be traced to an app (unknown, expired or tampered `state`): localized, `<realm>/pages/oauth-error/<locale>.html`, same security headers as the other hosted pages. A full hosted sign-in page (#158) is a separate design.

### 7.5 Apple revocation

Apple requires apps that offer Sign in with Apple and account deletion to revoke the user's tokens when the account is deleted. The redirect flow exchanges Apple's code; the ID-token endpoint exchanges the `authorization_code` the app sends. The refresh token is stored **encrypted** on the Apple identity, and on deletion (inside the erasure job) and on unlinking backd calls Apple's revoke endpoint through the restricted client, retries with the job `retry:`, then drops the token. The audit records that a revocation happened, never the token. `revoke_on_delete: false` disables storage and revocation. This is the single, deliberate exception to "no provider tokens are stored".

### 7.6 Profile handover

When a sign-in creates a user the response includes `profile: { name, given_name, family_name, picture }` (omitted fields absent). `account.on_signup: <db>/<fn>` (internal, async, retried, never failing the sign-up) is called for **every** new user, password sign-ups included, with `{ user_id, provider, profile? }`; the profile is masked in the function's logs and **nothing from it is stored by backd**. Apple sends the name only once, which is why the hook exists.

## 8. Accounts

### 8.1 The decision table

One pure function in `sso/decide.go`; the Directory supplies the facts.

| Situation | Realm provider | Organization connection |
|---|---|---|
| `(provider, org, issuer, subject)` exists | sign in as that user | same |
| not linked; an account has this email; the provider is trusted (`link: verified_email`: Google, Apple, GitHub, or `link_by_email`), the token's email is verified and the backd account is verified | **link automatically**, first sight only | not applicable |
| not linked; an account has this email; the provider is `link: verified_domain` and all conditions of §10.4 hold | not applicable | **link automatically**, the owner is emailed |
| not linked; an account has this email; any condition false | `account_exists` (+ `backd_provider`) | `account_exists` |
| not linked; no such account; realm sign-up `open` | create user and identity | create (see joining) |
| same; sign-up `invite` | only with a valid invitation passed to `start` whose bound email matches; else `invitation_invalid` | the connection creates users whatever the sign-up mode, because the realm switch is the operator's consent; the **restriction is the gate** |
| same; sign-up `closed` | `signup_closed` | as above |
| no email from the provider | `email_required` | `not_allowed` |
| `intent=link`, identity free | attach to the signed-in user | same |
| `intent=link`, identity taken by another user, or the user has one for this provider (and organization) | `link_conflict` | same |

- **Email verification of new users:** Google, Apple and GitHub with a provider-verified address: `email_verified: true` (an Apple relay address counts). Microsoft, `oidc` and `oauth2`: stored unverified and the normal verification flow applies, unless the provider has `link_by_email: true`. **Anything that comes through an organization's connection is stored unverified**: its managers control what the provider says, so a connection never lets them mark an address of this realm as verified.
- **Automatic linking happens only on first sight of an identity;** later changes of the provider's email never relink or change the user.
- **Pre-hijacking:** an attacker's unverified password account with the victim's address never absorbs the victim's Google identity (the backd account must be verified).
- **New users** get `locale` from the request like any sign-up. With `require_verified_email: true`, an unverified Microsoft, `oidc` or `oauth2` sign-in ends with `email_not_verified`.
- **Unlinking:** `DELETE /_auth/identities/{provider}` (organization identities take `?org=`) removes an identity, **never the last sign-in method**; an administrator can do the same for a user, with the same rule. A provider-only user who wants a password uses password reset.

### 8.2 Joining an organization

| Situation | Result |
|---|---|
| the user is not a member, not in `blocked` | joins with the connection's `join_roles`, only if this sign-in created the identity (first sight) or the user linked the connection |
| the user is in `blocked` | signed in, **not** joined |
| SCIM is enabled for the organization | the connection does not join anybody; [scim.md §3.5](./scim.md) applies |

- A manager removing a member (or a member removed by any path) adds them to `blocked` while a connection exists; accepting an invitation removes them from it. Leaving by oneself does not block.
- Sessions record the organization they were opened through (`via_org` on the login code and the challenges; the scope of the session in [scoped-sessions.md](./scoped-sessions.md)).
- **MFA:** the provider's own second factor is not trusted; the organization's `require_mfa` and the strategy's steps apply as for any member. `sso` is a strategy method ([auth-strategies.md §8](./auth-strategies.md)).

## 9. Organization connections

A **connection** is a provider configured for one organization: its client id (and issuer, for OIDC), a client secret kept as an organization-scoped secret, the restriction that says who may come in, and who joins with which roles. Signing in through it is the flow of §7; what is new is the address, the credentials, the restriction and the join.

### 9.1 Addresses

`_orgs` is the organizations API (session required); `_auth` is the sign-in API (no session). The sign-in address therefore reads `_auth/orgs/…`.

| Purpose | Address (relative to `/v1/{realm}`) |
|---|---|
| Sign in through a connection | `/_auth/orgs/{org}/oauth/{provider}/{start,callback,id-token}` |
| Providers a client may offer (public) | `/_auth/providers` and `/_auth/orgs/{org}/providers` |
| Which company is this email? | `/_auth/orgs/discover` (§10.5) |
| Manage a connection (managers) | `/_orgs/{org}/sso` and `…/sso/{provider}` |
| Manage a connection (realm admins) | `/_admin/orgs/{org}/sso` and `…/sso/{provider}` |
| Redirect address to register with the provider | `{public URL}/v1/{realm}/_auth/orgs/{org id}/oauth/{provider}/callback` |

`{org}` is the id; the routes that run before a session also accept the slug and resolve it inside the sign-in, so that nothing answers "does this slug exist?" ([scoped-sessions.md §5.3](./scoped-sessions.md)).

### 9.2 What admits a person

The restriction is **cryptographic or DNS-proved, never claimed**, and is checked on the verified token:

| Provider | Required |
|---|---|
| `google` | the token's `hd` equals the connection's `hosted_domain` (a consumer account has no `hd` and is refused), and the email is verified |
| `microsoft` | the token's `tid` equals the connection's `tenant` (a GUID; `common`, `organizations` and `consumers` are refused), and the address is on the domain of the token's `preferred_username` (§11.2) |
| `oidc` | the connection has at least one **verified domain** (§10) and the token's `email` is present, `email_verified` is true, and its domain **equals** one of the connection's domains. The issuer vouches for the *person*; the DNS proof is what ties the *address* to the company. A lapsed domain stops admitting at once |

`email_domains` (an extra restriction) may only name verified domains once domains are on. Nothing is admitted on a claim alone: with `oidc` the connection cannot even be enabled without a verified domain.

### 9.3 The connection

`org_connections` (auth data, in the realm's system database):

| Field | Notes |
|---|---|
| `id`, `org`, `provider` | unique on (`org`, `provider`); `id` is `<org>:<provider>`; `provider` is `google`, `microsoft` or `oidc` |
| `client_id` | the organization's application with the provider |
| `issuer` | `oidc` only: must match an allow-list pattern (§5.2). Changing it is a full re-validation (§6.1) |
| `tenant` | Microsoft: a tenant id (GUID) |
| `hosted_domain` | Google: the Workspace domain |
| `domains` | `oidc` (and an optional narrowing for the others): the verified domains this connection admits; default all of the organization's |
| `native_client_ids` | extra audiences for the ID-token endpoint (Google, Microsoft) |
| `join_roles` | roles given on joining, checked with the granting rules of [organizations.md §5.1](./organizations.md): `manage: roles` any, `manage: members` the roles that open nothing the setter lacks, `manage: sso` alone none; at set time and at every join |
| `enabled`, `blocked` | `blocked` is the ids of members removed by a manager, newest 1000 |
| `created_by`, `updated_by`, timestamps | `updated_by` is whose capabilities `join_roles` were checked against; a connection whose setter loses the roles is switched off before any account is made (`org.sso.disabled`) |

- **Management API:** `GET /_orgs/{org}/sso`, `GET|PUT|DELETE …/sso/{provider}` (`client_secret` in the body when set or changed). Changes need `manage: sso` and reauth; each is audited (`org.sso.set`, `org.sso.deleted`) without the secret. Realm admins have the same under `/_admin/orgs/{org}/sso` and `backd orgs sso`, `sso-set`, `sso-delete`. The response never contains the secret: whether it is set, and the redirect address to register.
- **One validator:** the field rules (client id patterns, GUID tenants, issuer syntax) are the functions `internal/registry` shares between `realm.yaml` and connections.
- **Providers a client may offer:** `GET /_auth/providers` and `GET /_auth/orgs/{org}/providers` share one handler and answer `{"items": ["google"]}`: only providers that are configured *and* usable right now by the same check `start` uses. An unknown organization, one without connections and one with SSO off all answer `{"items": []}`; cacheable for a minute; the sign-in rate limits apply.
- **Unavailable connections:** unknown, disabled, no secret, organization not `active`, SSO off, no verified domain (`oidc`) all answer the same `404` from `start`. The realm's own providers keep `503 provider_unavailable`. `503` is also what an unknown public address of backd gives, the same for every organization.

### 9.4 Organization secrets

A third scope of the existing secrets store: **organization**, owned by the realm, written by the organization's managers through the connection endpoint, **never readable by anyone** (not by them, not by functions); realm admins can replace and delete them.

- Storage: the same collection and cipher as realm and database secrets. The document has `org` and its `database` field holds the scope (`org:<id>`), so the unique `(database, name)` index holds one per organization.
- Names are fixed (`SSO_GOOGLE_CLIENT_SECRET`, `SSO_MICROSOFT_CLIENT_SECRET`, `SSO_OIDC_CLIENT_SECRET`). `registry.SecretRef` gains `Org`, set only by the sign-in code; `function.yaml` has no syntax for it, so functions cannot reach them.
- Realm admins: `backd secret set --realm r --org ORG NAME`, `list` (names and dates), `delete`. Rotation of the instance key re-seals every scope.

### 9.5 Lifecycle and portability

- **Organization deleted or not active:** connections, domains and secrets go with the deletion (the existing resumable job); an `orphaned` organization's connections answer unavailable. Identities bound to the organization stay (they are the user's) but cannot sign in once the connection is gone.
- **User erased:** their organization identities go with them.
- **Export and import:** `org_connections` and `org_domains` are auth parts (`--include auth`). Secrets never travel: after an import a connection answers unavailable until its secret is set. Domain claims travel as `pending` and must be proved again; anonymized packages blank client ids and issuers, and carry no domain claims (a copy for staging proves nothing, and several blank domains would collide). An import validates connections with the API's rules (a blank client id is allowed in anonymized packages).

## 10. Verified domains

A **verified domain** is a domain an organization has proved it controls by publishing a DNS record that backd checks. Nothing else in organization sign-in proves that an address belongs to a company: the Workspace `hd` and the Microsoft `tid` prove the *company of the person*, and a free-text domain list is only a claim. With the proof, four things become safe: an organization's **OIDC connection** can be admitted (§9.2); a sign-in on one of its domains can **link** to an existing account (§10.4); an application can ask **which organization an email belongs to** (§10.5); and **SCIM** can provision only addresses the organization owns ([scim.md](./scim.md)).

### 10.1 Decisions

| Topic | Decision |
|---|---|
| Proof | A DNS **TXT** record that backd checks. No approval by the realm operator |
| Match | **Exact**: an address belongs to a verified domain only if its domain equals it. A verified `acme.example` does not cover `eng.acme.example`; that one is verified on its own |
| Exclusivity | A domain is **verified for one organization per realm** (first to pass the check wins). Free-mail domains and public suffixes are refused |
| Re-check | Re-checked periodically (default every **7 days**); after **3** consecutive failures the domain **lapses** (about three weeks) and linking, discovery and SCIM provisioning pause for it |
| Linking | Automatic, only when **all** the conditions of §10.4 hold; the person is told by email. Each domain has a `link_accounts` switch (default on) so an organization can keep proof and discovery without linking |
| Discovery | **Opt-in per organization** (`discoverable`, default off); uniform answer for everything else |
| Who manages | A new capability `manage: domains` with reauth |
| Where | `org_domains` collection; realm switch `organizations.domains` |

Realm switch: `organizations.domains` (§5.2). With it off the whole surface answers `404`, and an organization cannot enable an `oidc` connection. The built-in refusal list holds the common free-mail providers (Gmail, Outlook/Hotmail/Live, Yahoo, iCloud, Proton and so on) and every **public suffix** (`com`, `co.uk`); the realm's `blocked` adds to it. `resolvers` pins trusted DNS servers; the default is the host's resolver, which in a production profile gives a startup warning and a line in the production checklist.

### 10.2 Persistence

A new collection `org_domains` in the realm's system database, created and validated by `backd provision`.

```json
{
  "_id": "cr3s0k5v1d9u2n8e7a0g:acme.example",
  "org": "cr3s0k5v1d9u2n8e7a0g",
  "domain": "acme.example",
  "state": "verified",
  "challenge": "backd-verify=7Qm2c1xJg9vKf0aTz3LwP8nRb5uYdE6h",
  "discoverable": true,
  "link_accounts": true,
  "failures": 0,
  "created_by": "cr3rvu1i8d2f4n0b9k7g",
  "created_at": "2026-11-02T09:00:00Z",
  "verified_at": "2026-11-02T09:12:30Z",
  "last_checked_at": "2026-11-30T03:00:00Z",
  "next_check_at": "2026-12-30T03:00:00Z"
}
```

| Field | Notes |
|---|---|
| `_id`, `org`, `domain` | `<org>:<domain>`; the domain is lowercased and stored as ASCII (punycode) |
| `state` | `pending` (challenge issued, not yet seen), `verified`, `lapsed` (was verified, the record is gone) |
| `challenge` | the TXT value to publish: 128 random bits. It is public once published, so it is stored in clear |
| `discoverable` | the organization lets its domain be used for discovery (§10.5); default `false` |
| `link_accounts` | the organization lets sign-ins on this domain link existing accounts (§10.4); default `true`. Off keeps proof, SCIM and discovery but answers `account_exists` as before |
| `failures` | consecutive failed re-checks |
| `verified_at`, `last_checked_at`, `next_check_at` | the re-check job's schedule |
| `created_by` | the one who started the claim |

Indexes: (`org`, `domain`) unique; **`domain` unique, partial where `state` is `verified`** (this is what makes the domain exclusive: two organizations may both be `pending` for it, only one can become `verified`); (`next_check_at`) for the job.

Other changes: none to `org_connections` (its `email_domains` is validated against verified domains when set, §9.3). `org_domains` is an auth part for export and import, but **claims travel as `pending`** and must be proved again in the new realm; anonymized packages blank `domain` and `challenge`. Deleting the organization removes its domains and releases them.

### 10.3 Proof

1. A manager (`manage: domains`, fresh proof of identity) starts a claim: `POST /_orgs/{org}/domains { "domain": "acme.example" }`. backd normalizes it, refuses free-mail and public suffixes, and answers `201` with the record to create:

   ```json
   { "domain": "acme.example", "state": "pending",
     "record": { "type": "TXT", "name": "_backd-challenge.acme.example", "value": "backd-verify=7Qm2c1xJg9vKf0aTz3LwP8nRb5uYdE6h" } }
   ```

2. The manager publishes the record and calls `POST …/domains/{domain}/verify`, or waits: pending claims are checked by the job at growing intervals for a limited time (then they stay `pending` until verify is called).
3. A check asks the resolver for the TXT records of `_backd-challenge.<domain>` and looks for the exact `challenge` value. On success the state becomes `verified` **if no other organization holds the domain** (the unique index decides a race); otherwise `409 domain_taken`.
4. A verified domain is re-checked every `recheck`. A failed check increments `failures`; at `lapse_after` the state becomes `lapsed`, the managers are emailed, and `org.domain.lapsed` is audited. A later pass returns it to `verified`; while `lapsed` another organization may claim and verify it.

Limits: `max_per_org` claims, 6 verify calls an hour per domain, and the check job is bounded (below).

### 10.4 Linking

At the end of an organization sign-in (§8.1), an account is linked to the identity **instead of answering `account_exists`** only when **all** hold:

1. the sign-in came through a connection of organization **X** whose restriction is the Workspace `hd`, the Microsoft `tid` or, for `oidc`, the verified domain itself (every connection has one);
2. the provider's token says the email is **verified** (Google `email_verified`; Microsoft the UPN-domain rule; `oidc` `email_verified`);
3. the email's domain **equals a `verified` domain of X** (not `pending`, not `lapsed`) whose `link_accounts` is on;
4. an account with that email exists and **its email is verified in backd** (a never-confirmed address is not enough);
5. that account has **no identity for the same provider and organization** yet.

When it holds: the identity `(provider, X, issuer, subject)` is created for that account, the sign-in continues as a normal one (the session is scoped to X; `join_roles`, or the provisioned-only rule when SCIM is on), `org.sso.linked` is audited with `by: domain`, and the account's owner gets an email: *"Acme's Microsoft sign-in was linked to your account. If it was not you, …"*. When it does not hold, the sign-in answers `account_exists` and the person links explicitly (`intent=link`).

Why this is acceptable: whoever controls a domain already controls the email of every address on it, and so can reset the password of any account there. Linking adds no new power beyond that, and it is further bounded by [scoped-sessions.md §7](./scoped-sessions.md): a session opened through a connection cannot change credentials, MFA, email or API keys, nor erase or export the account, so the link gives access to **the organization's data** and not to the person's other organizations or to the account itself. A lapsed domain stops linking at once; identities already linked stay, but still need the connection to be enabled.

### 10.5 Discovery

`POST /v1/{realm}/_auth/orgs/discover { "email": "ana@acme.example" }` (no session) answers:

```json
{ "items": [ { "org": "cr3s0k5v1d9u2n8e7a0g", "name": "Acme", "providers": ["microsoft"] } ] }
```

- Only for a domain that is `verified`, `discoverable: true`, and whose organization has an **enabled** connection. The application then starts the usual `…/orgs/{org}/oauth/{provider}/start`.
- **Uniform otherwise:** an unknown domain, an unverified one, one that is not discoverable and one without a connection all answer `{ "items": [] }`, with the same status, shape and timing. It never says whether an account or a member exists, only which connection the domain's organization offers.
- **Limits:** like "which companies am I in?" ([scoped-sessions.md §5.4](./scoped-sessions.md)): per IP and per address, from the shared counters. `discoverable` is the organization's choice (`PATCH …/domains/{domain}`, `manage: domains`), default off, because it tells the world that the company uses the application.

### 10.6 Security of domains

- **DNS is the trust anchor.** A spoofed TXT answer would verify a domain that is not the organization's. Mitigations: a configurable `resolvers` list (operators can pin DoH/trusted resolvers), a 128-bit challenge only the claimant knows, re-checks, and the fact that a forged proof only helps while the attacker also controls a tenant (`tid`/`hd`) whose users sign in to this organization's connection. DNSSEC validation is not required and the docs say so.
- **Egress is bounded:** the only outbound traffic is a TXT lookup for `_backd-challenge.<domain>` with a 3-second timeout, at most 10 answers of at most 255 bytes, no recursion into other protocols, per-domain and per-organization rate limits, and a bounded job. The domain name is validated (no IP literals, no internal-only names, no public suffixes) before it is looked up.
- **Domain takeover:** an expired domain bought by someone else stops passing the check, lapses after `lapse_after` failures, and the new owner can then claim it. The window is `recheck × lapse_after` (about three weeks by default); operators may change both. Linking, discovery and SCIM stop on lapse.
- **Race and squatting:** two organizations can both be pending; the unique partial index lets exactly one become verified. A claim never blocks the real owner for long: only a *verified* claim is exclusive, and it needs the record.
- **Linking is a notification event:** the account owner is always emailed; the audit has `org.sso.linked`.
- **Privacy:** discovery is opt-in, uniform and rate-limited; it reveals a connection, not people.
- **Update** `docs/content/docs/operate/administration/security.md` and `SECURITY.md` in the same milestone.

### 10.7 API, CLI, clients

Paths relative to `/v1/{realm}`; managers need `manage: domains`; changes need a fresh proof of identity.

| Request | Answer |
|---|---|
| `GET /_orgs/{org}/domains` | `200` the claims with state, record to publish, `discoverable`, dates |
| `POST /_orgs/{org}/domains` `{domain}` | `201` the claim with the record; `409 domain_taken`, `422` refused domain, `409 domains_limit` |
| `POST /_orgs/{org}/domains/{domain}/verify` | `200` the claim (verified or still pending, with the reason: record not found, mismatch, resolver error) |
| `PATCH /_orgs/{org}/domains/{domain}` `{discoverable?, link_accounts?}` | `200` |
| `DELETE /_orgs/{org}/domains/{domain}` | `204`; what depended on it (SCIM-provisioned addresses) is listed in the answer of the call and keeps working until the next sync refuses it |
| `POST /_auth/orgs/discover` `{email}` | `200` `{items}` (§10.5) |
| the same under `/_admin/orgs/{org}/domains` | realm administrators; a force removal for a dispute |

**CLI:** `backd orgs domains`, `domain-add`, `domain-verify`, `domain-remove`. **Clients:** `orgs.domains(org)` with `list/add/verify/update/remove`, and `auth.discover(email)`; the Dart client gets the same. **Admin UI:** a "Domains" tab in the organization page. **Panel:** a card in the example settings page ([scim.md §11.6](./scim.md)).


## 11. Security

### 11.1 Network

Provider calls from `serve` (token exchange, discovery, JWKS, userinfo) and from the worker (Apple revocation) use `sso/safehttp`, **not `backd egress`** (which serves the functions executor). The client:

- dials only the provider's **allowed hosts**: built in for Google (`accounts.google.com`, `oauth2.googleapis.com`, `www.googleapis.com`), Microsoft (`login.microsoftonline.com`), Apple (`appleid.apple.com`) and GitHub (`github.com`, `api.github.com`); for `oidc`, the issuer's host plus the hosts of the discovery document's endpoints **only when they share the issuer's registered domain** (public suffix list); for `oauth2`, the hosts of the three configured URLs. Any other destination is refused, **including a redirect to one**;
- is `https` only (`http` only under `BACKD_DEV`), resolves names itself and dials the address it resolved (no second lookup, so no DNS-rebinding window);
- refuses **private, loopback, link-local and metadata addresses** (`127/8`, `10/8`, `172.16/12`, `192.168/16`, `169.254/16`, `100.64/10`, `::1`, `fc00::/7`, `fe80::/10`) except under `BACKD_DEV`;
- caps every response (1 MiB), the number of redirects (none), the time (10 s) and the discovery and JWKS documents' key counts.

**Private identity providers.** A provider the operator writes in `realm.yaml` may set `allow_private_network: true`: its own allowed hosts may then resolve to private ranges. Loopback, link-local and the metadata address stay refused, and the setting does not widen the host list. Startup logs a warning for each provider that sets it. **Organization connections can never set it:** an issuer an organization names is always public.

**Organization issuers** must match the realm's allow-list (§5.2) at creation and at every discovery refetch; the checks above apply on top. A discovery document whose `issuer` differs from the connection's, or that names an endpoint outside the registered domain, makes the connection unavailable. Discovery documents and JWKS are cached following their headers; an unknown signing key triggers a refetch **at most once a minute** per provider.

### 11.2 Verifying an ID token

All OIDC kinds go through one verifier (`sso/oidc`):

- **Signature:** only `RS256`, `PS256`, `ES256` and `EdDSA`. `none` and every `HS*` are refused, so a client secret can never be used to forge a token. The header's `jku`, `jwk`, `x5u` and `x5c` are ignored: keys come only from the provider's JWKS (`use: sig` or absent), matched by `kid`.
- **Claims:** `iss` equals the provider's issuer exactly (for Microsoft multi-tenant, `https://login.microsoftonline.com/<tid>/v2.0` with the token's own `tid`, and `tenant` restricts which tenants are accepted; a templated issuer such as Entra's `common` is not accepted by `oidc` — use `microsoft`); `aud` contains the client id (or a configured native audience), and when it has several values `azp` must equal the client id; `exp` in the future, `nbf` and `iat` not in the future, with 60 seconds of leeway; `nonce` equals the attempt's (always required).
- **Email:** `email_verified` may arrive as a boolean or the string `"true"`; absent counts as false. The address is normalized (`NormalizeEmail`) and one with two `@` is refused. **Microsoft:** an address is accepted only on the domain of the token's `preferred_username` (the user principal name, whose domain a tenant has to verify); a token without it, or with another domain, is `not_allowed`.
- **Google:** `hd` is checked against the connection's `hosted_domain`; **Apple:** the relay address counts as verified.
- **Size:** a token over 16 KiB is refused before parsing.

OAuth 2 kinds have no token to verify: the userinfo is fetched over TLS from the allowed hosts with the access token, parsed with a size cap, and mapped through `claims`.

### 11.3 Checklist

- **`state`** (single use, 10 minutes, stored hashed) protects the callback against forgery and **binds provider and scope**; a nonce is in every ID token; PKCE protects the code between backd and the provider (where the provider supports it) and between the app and backd (required). When a provider sends the `iss` parameter on the callback (RFC 9207), it must equal the provider's issuer (mix-up attacks).
- **`redirect_to`** only from `allowed_redirects`, stored server-side with the attempt, never taken from the callback.
- **Login codes:** 1 minute, single use, hashed, PKCE-bound; sessions never in URLs.
- **Automatic linking** only under §8.1 / §10.4, and only on first sight of an identity.
- **Rate limits:** 30 per minute per IP on `start`, `token` and `id-token`; 60 per minute on `providers`; the shared counters of [rate-limiting.md](./rate-limiting.md).
- **Secrets:** provider secrets in the encrypted store; organization secrets sealed and never returned, logged, exported or readable by functions; Apple refresh tokens encrypted; tokens, codes and secrets never logged (tests scan the log and the audit trail).
- **Enumeration:** every failure to resolve a connection answers the same; the provider list of an organization answers `{"items": []}` for everything that is not usable.
- **Uniform failure to the user:** a refused sign-in says `signin_refused` or `not_allowed`; the audit has the reason.

### 11.4 Threats this design closes

| # | Threat | Control |
|---|---|---|
| T1 | **A changed issuer inherits identities.** `sub` is unique only per issuer; an issuer edit could let a stranger match an existing user's identity | Identities record their issuer and match on it (§6.1) |
| T2 | **A manager's own OIDC server asserts any email** and lands in the user table, or links an account | A connection with no verified domain cannot be enabled; the email must be verified and on a verified domain (§9.2); stored unverified; links only under §10.4 |
| T3 | **SSRF through an issuer** (cloud metadata, internal services) | Allow-list, https, same-registered-domain discovery, public addresses only, dial the resolved address, no redirects (§11.1) |
| T4 | **A wildcard on a public suffix** (`https://*.com`) admits anyone's server | Refused at load; one label only |
| T5 | **GitHub login renamed and reused** | The subject is the numeric id; the startup check refuses mutable names as an `oauth2` subject |
| T6 | **An unverified email from a plain OAuth 2 service links an account** | `oauth2` never links unless `link_by_email` and an explicit, mapped, true verified flag |
| T7 | **An organization pre-empts a mailbox through its tenant** (milestone-30 review R1) | A Microsoft address counts only on the domain of `preferred_username`; stored unverified |
| T8 | **`join_roles` escalation** (R2) | Checked with the granting rules at set time and at every join; a setter who loses the roles switches the connection off |
| T9 | **A token issued for one connection used at another** | State binds the scope; at the ID-token endpoint the audience and issuer must match the connection; two connections naming the same client id and issuer are the managers' own configuration (accepted, A4 below) |
| T10 | **Algorithm confusion** (`HS256` with the client secret, `none`) | Fixed allow-list of asymmetric algorithms |
| T11 | **Login CSRF / code injection** | State plus PKCE-bound login codes |
| T12 | **Open redirect** | Allow-listed `redirect_to`, stored with the attempt |
| T13 | **Lock-out by unlinking** | The last sign-in method cannot be removed |
| T14 | **Domain proof forged by DNS spoofing** | Pinned resolvers, a 128-bit challenge, re-checks; the forged proof helps only a manager whose provider also authenticates the address (§10.6) |

### 11.5 Carried forward from the milestone-30 review

| # | Finding | Result |
|---|---|---|
| R3 | Imported connections were trusted blindly | An import validates them with the API's rules |
| R4 | `email_domains` compared the raw address | Both sides use `NormalizeEmail` |
| R5 | Organization secrets and the unique index | `(database, name)` with the scope in `database` |
| A1 | The provider list of an organization tells whoever guesses a slug that it has a connection | **Accepted:** what an app needs to show buttons; every other failure answers the same, ids are random, 60 a minute per address, operators who keep customer names secret choose unguessable slugs |
| A2 | A manager with a tenant can create as many accounts as they have users, in a `closed` realm too | **Accepted:** the realm turned the feature on; each sign-in is audited; deleting the connection stops it. A per-organization member cap is not part of this design |
| A3 | The blocked list keeps the newest 1000 and only blocks while a connection exists | **Accepted:** documented; an invitation is the supported way back |
| A4 | A token issued for one connection's client id is accepted by another connection with the same client id, issuer and domain | **Accepted:** both are the managers' own configuration; to hold such a token a person must already be an admitted member of the company |

Checked and found sound in that review and kept: state bound to its organization; identical unavailable answers; the realm's trust of an address is false for connections, so a known address is `account_exists` even when Google says verified (§8.1 changes this only for verified domains); no issuer URL accepted from managers outside the allow-list; list sizes bounded; removed members blocked.

## 12. Endpoints, errors, audit

### 12.1 Endpoints

Paths are relative to `/v1/{realm}`.

| Method and path | Purpose |
|---|---|
| `GET`, `POST /_auth/oauth/{provider}/start` | begin sign-in (`GET` → `302`), or sign-in/linking (`POST` → `{authorize_url}`) |
| `GET`, `POST /_auth/oauth/{provider}/callback` | provider callback |
| `POST /_auth/token` | redeem the one-time login code |
| `POST /_auth/oauth/{provider}/id-token` | native sign-in (OIDC kinds with native audiences) |
| `GET /_auth/providers` | the providers a client may offer now |
| `GET /_auth/me` | + linked identities (provider, organization, email, linked at) |
| `DELETE /_auth/identities/{provider}` | unlink (never the last method); `?org=` for organization identities |
| the same `start`, `callback`, `id-token` and `providers` under `/_auth/orgs/{org}/…` | organization sign-in (§9.1) |
| `POST /_auth/orgs/discover` | which organization is this email (§10.5) |
| `GET /_orgs/{org}/sso`, `GET\|PUT\|DELETE /_orgs/{org}/sso/{provider}` | manage connections (`manage: sso`, reauth) |
| `/_orgs/{org}/domains…` | verified domains (`manage: domains`, reauth; §10.7) |
| `GET /_admin/users/{id}/identities`, `DELETE …/{provider}` | admin view and unlink |
| `/_admin/orgs/{org}/sso…`, `/_admin/orgs/{org}/domains…` | realm administrators |

### 12.2 Error codes

On the callback, as `?backd_error=<code>`; on JSON endpoints, the same code as the error `code`:

`cancelled`, `account_exists` (+ `backd_provider`), `signup_closed`, `invitation_invalid`, `link_conflict`, `email_not_verified`, `email_required`, `signin_refused`, `provider_error`, `not_allowed` (organization restriction or verified-domain check failed), and for JSON `invalid_token`, `invalid_redirect`, `provider_unavailable`.

### 12.3 Audit

Existing: `identity.linked`, `identity.unlinked` (also by an administrator, who is the actor), `identity.apple_revoked`, `identity.signin_refused` (the real reason), `org.sso.set`, `org.sso.deleted`, `org.sso.login`, `org.sso.disabled`, and the `org.member_add` with `by: sso` for a join. Provider sign-ins are recorded by the entries of [sign-in-audit.md](./sign-in-audit.md). New: `org.sso.linked` (`by: domain`), `org.domain.added`, `org.domain.verified`, `org.domain.lapsed`, `org.domain.removed`, `org.domain.settings`. No entry ever carries a token, code or secret.

## 13. Clients, tools, documentation

- **Clients (JS, Dart, Go, Python):** `signInWith(provider, { redirectTo, org? })` (PKCE handled; the page that receives `backd_code` completes the sign-in, `completeOnLoad`), `linkProvider`, `unlinkProvider`, `identities`, `signInWithIdToken` (native), `providers({ org? })`, `discover(email)`, and `orgs.sso` / `orgs.domains` helpers. Errors arrive as typed `BackdError`s. The JavaScript client has the `org` parameters today; Dart, Go and Python get them (the gap of milestone 30).
- **CLI:** `backd orgs sso`, `sso-set`, `sso-delete`; `backd orgs domains`, `domain-add`, `domain-verify`, `domain-remove`; `backd secret … --org`; `backd user identities`. Every value is a flag.
- **Admin UI:** the organization page has a *Sign-in* section (the connections, the redirect address to register, who joined through them, the last twenty `org.sso.login` events) and a *Domains* section.
- **Docs:** "Sign in with providers" (consoles, callback URL, `auth.providers`, secrets, redirect flow with the client, native sign-in, GitHub and OAuth 2, generic OIDC, linking rules and why Microsoft and generic emails do not auto-link, error codes, profile and `on_signup`, Apple revocation); the organization guides (connections, OIDC and the allow-list, verified domains, the sign-in experience); the operator page for `organizations.sso` and `organizations.domains`; the security pages and `SECURITY.md` (§11); `api/openapi.yaml` for every route and code.
- **Testing with real providers:** [identity-providers-testing.md](./identity-providers-testing.md) gains GitHub, a Keycloak and an Okta recipe; real-provider runs stay manual before a release that touches sign-in.

## 14. Rollout

Pre-1.0: breaking changes are allowed and listed first in the changelog. The milestone is named when the issues are written; the stack, in order:

| Step | Content | Notes |
|---|---|---|
| S1 | **Extract `sso/`** with no behavior change: `internal/oauth` → `sso/oidc`, `oauth2`, `apple`, `safehttp`, `pkce`; the ports; the engine; the decision table as one function; `httpsso` handlers; `ssotest`; the import-guard test. The 1,200 lines of handler code become thin | The existing tests move with it and must pass unchanged |
| S2 | **Issuer on identities:** the field, the new unique indexes, the `backd provision` backfill | The one data step; `PROVISION_MODE=verify` refuses to start until run |
| S3 | **Generic OIDC (realm):** `type: oidc`, discovery, `link_by_email`, `allow_private_network`, the registered-domain rule | Closes #157 |
| S4 | **GitHub and `oauth2`** | Audit B6 |
| S5 | **Verified domains:** `org_domains`, proof, re-check job, `manage: domains`, API, CLI, linking, discovery | Moves #601 here from the SCIM milestone; SCIM's S-b is this step |
| S6 | **Organization OIDC:** the allow-list, the `oidc` connection, the verified-domain gate, secrets name | Closes #599; #598 closes as won't-do |
| S7 | **Clients and admin UI:** four clients, the *Sign-in* and *Domains* sections | |
| S8 | **Docs, security pages, review** | |

**Breaking changes:** `identities` gain `issuer` (run `backd provision`); realm providers named `github`, `oidc`, `oauth2` or `sso` are now reserved; an organization connection of kind `oidc` is new, not a change. Nothing in the HTTP API changes for existing clients.

**Cross-design amendments** made together with this document: [scim.md](./scim.md) and [scoped-sessions.md](./scoped-sessions.md) link here instead of to the two old designs; the "Milestone 31" text is gone (Apple for organizations is not built; generic OIDC is S3 and S6; verified domains is S5; `sso_required` was built by milestone 34). [auth-strategies.md](./auth-strategies.md) lists `github` and generic provider names as methods.

## 15. Alternatives considered

- **Keep protocol-only `internal/oauth`:** the account rules and the 1,200 handler lines stay, and every new kind adds a special case to each layer.
- **A library under `internal/`:** the same split without a public promise; chosen against so the library can move out like `kli` and be reused.
- **Any public https issuer for organizations / operator approval of each issuer:** the first lets tenants steer backd's outbound requests; the second adds a support workflow. The allow-list keeps the decision with the operator and the onboarding self-service.
- **OIDC connections admitted on issuer identity only, or with a free-text domain list:** a manager's server can assert any address; only a DNS-proved domain ties the address to the company.
- **Forbidding issuer changes instead of binding identities to the issuer:** still leaves `realm.yaml` edits unprotected.
- **Apple, GitHub and plain OAuth 2 for organizations:** no company proof (see §4.1); revisit per kind with its own proof.
- **Never auto-link / always auto-link:** chose the verified-and-trusted rule (Google, Apple, GitHub; generic only by the operator's explicit choice).
- **Secrets in environment variables or `realm.yaml`:** chose the encrypted store.
- **Storing profile fields:** breaks "auth data only" and escapes schemas and erasure policies.
- **Provider calls through `backd egress`:** egress is built for the functions executor (per-invocation tokens); chose a restricted client.
- **A short link token for linking, or no linking:** rejected.
- **A hosted page per outcome / a full hosted sign-in page:** app-handled errors plus one fallback page; the full page is #158.
- **Leaving Apple revocation to apps:** it would reintroduce per-app server work and App Store risk.
- **SAML:** not built (decisions, 2026-11-11); OIDC and SCIM instead.
- **`/org/{id}/_auth/…` addresses:** can clash with a database called `org`; `_auth` is reserved and unauthenticated.
- **One realm callback with the organization only in the state:** the address a manager registers would be identical for every organization.
- **Organization secrets through field encryption:** needs a whole milestone for a handful of values the secrets store already seals.
- **Prefix-matching verified domains, email-proof of a domain, realm-admin approval:** exact match is predictable; email proof is weaker; approval was rejected by decision.

## 16. What this document replaces

| Old document | Where it went |
|---|---|
| `identity-providers.md` §1–2, 4–13 | §1, §2, §6, §7, §8, §11, §12 |
| `identity-providers.md` §3 (config), §3.1 (OIDC) | §5.1, §4, §11.1 |
| `identity-providers.md` §14–17 (docs, acceptance, alternatives, follow-ups) | §13, §14, §15 |
| `org-sso.md` §1–2.1 | §1, §2, §9.1 |
| `org-sso.md` §3 (organization secrets) | §9.4 |
| `org-sso.md` §4 (connections), §5 (signing in), §6 (accounts), §7 (lifecycle) | §9.3, §9.2, §7, §8, §9.5 |
| `org-sso.md` §8 (clients), §9 (security), §14 (review) | §13, §11 |
| `org-sso.md` §12 (milestone 31), §13 (as built) | §14; what was built is now the baseline of §4–§9 |
| `verified-domains.md` | §10 (linking generalized to the `oidc` connection; discovery and API unchanged) |
| an earlier local draft of `identity-providers.md` (2026-10-04, never committed) | retired: it no longer matched the code (`/oauth/token`, `?error=`, `backd egress`) |

The designs that still apply and are **not** replaced: [scim.md](./scim.md), [scoped-sessions.md](./scoped-sessions.md), [auth-strategies.md](./auth-strategies.md), [mfa-passwordless.md](./mfa-passwordless.md), [passkeys.md](./passkeys.md), [sign-in-audit.md](./sign-in-audit.md).
