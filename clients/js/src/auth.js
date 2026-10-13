import { AuthenticationError, ValidationError, VerificationRequiredError } from './errors.js'
import { COOKIE_SESSION } from './storage.js'
import { defaultOAuthStorage, loadPending, newPKCE, oauthError, parseReturn, savePending, withoutReturn } from './oauth.js'

/**
 * @typedef {import('./client.js').Client} Client
 * @typedef {import('./client.js').RequestOptions} RequestOptions
 */

/**
 * A realm user as the user sees themselves.
 * @typedef {object} User
 * @property {string} id
 * @property {string} email
 * @property {boolean} email_verified
 * @property {string[]} roles
 * @property {string} locale       The user's language, one the realm lists (used for their emails).
 * @property {string} created_at   RFC3339 timestamp.
 */

/**
 * One way the user signs in: a password or an external provider.
 * @typedef {object} Identity
 * @property {string} provider        `password`, `google`, `microsoft`, `apple` or a configured provider's name.
 * @property {string} email           The address the provider reported (the user's own for a password).
 * @property {boolean} email_verified
 * @property {string} created_at      RFC3339 timestamp: when it was linked.
 * @property {string | null} last_used_at   RFC3339 timestamp of the last sign-in with it, null if none is recorded.
 */

/**
 * The signed-in user with their sign-in methods, oldest first.
 * @typedef {User & { identities: Identity[], reauth_methods: Array<'totp' | 'recovery_code' | 'password' | 'email_code'> }} Me
 */

/**
 * What a provider says of a person who just signed up, handed over once: fields the provider
 * did not give are absent. Treat it as a hint.
 * @typedef {object} Profile
 * @property {string} [name]
 * @property {string} [given_name]
 * @property {string} [family_name]
 * @property {string} [picture]   A URL.
 */

/**
 * A new session.
 * @typedef {object} Session
 * @property {string} [token]     Session token (`bds_…`); already stored by the client. Absent with `cookies: true`:
 *   the session is in an HttpOnly cookie, out of reach of scripts.
 * @property {'Bearer'} [token_type]
 * @property {string} session_id
 * @property {string} expires_at  RFC3339; pushed forward as the session is used.
 * @property {User} user
 */

/**
 * The session a sign-in with a provider gives: a login's, and whether it made the user.
 * @typedef {Session & { new_user: boolean, profile?: Profile }} ProviderSession
 */

/**
 * Options of the redirect sign-in.
 * @typedef {object} SignInWithOptions
 * @property {string} [redirectTo]    Where backd sends the browser back to (it must be within the realm's
 *   `sign_in.allowed_redirects`); this page, without its query, by default, and outside a browser
 *   the realm's `sign_in.app_url`.
 * @property {string} [invitation]    An invitation token, for realms with `signup: invite`.
 * @property {string} [locale]
 * @property {(url: string) => void | Promise<void>} [navigate]  How to go to the provider; `location.assign` by default.
 *   Apps that open a browser session (React Native, Capacitor) pass their own.
 */

/**
 * @typedef {object} SessionInfo
 * @property {string} id
 * @property {string} created_at
 * @property {string} last_used_at
 * @property {string} expires_at
 * @property {boolean} current  The session making this request.
 */

/**
 * What changed: a sign-in (signup or login), a sign-out (logout, logout
 * everywhere, account deleted), or the server refusing the stored token
 * (expired, revoked, or the user disabled).
 * @typedef {'SIGNED_IN' | 'SIGNED_OUT' | 'SESSION_EXPIRED'} AuthEvent
 */

/**
 * @callback AuthListener
 * @param {AuthEvent} event
 * @param {Session | null} session  The new session for SIGNED_IN, otherwise null.
 * @returns {void}
 */

/** A body without the fields that were not given. @param {Record<string, unknown>} fields */
function compact(fields) {
  return Object.fromEntries(Object.entries(fields).filter(([, v]) => v !== undefined))
}

/** Sign-up, login and session management: `client.auth`. */
export class Auth {
  /** @type {Promise<ProviderSession | { linked: string } | null> | undefined} */
  #completed = undefined

  /** @param {Client} client */
  constructor(client) {
    /** @internal */
    this.client = client
    /** @internal */
    this.listeners = new Set()
    /** @type {import('./oauth.js').OAuthStorage | undefined} */
    this._oauthStorage = undefined
    /**
     * The outcome of the sign-in the page was opened to finish, if any: resolves with the session
     * (`new_user`, `profile`), `{ linked }`, or `null` for an ordinary visit; rejects with the
     * `BackdError` of a failed sign-in (`wrong_device` when the page is not where it was started).
     * It never settles before the client is created and is the same promise for every reader.
     * @type {Promise<ProviderSession | { linked: string } | null>}
     */
    this.ready = Promise.resolve(null)
  }

  /**
   * Finishes the sign-in the page's address carries, once, when the client is created (see the
   * `completeOnLoad` option); the outcome is {@link Auth.ready}.
   * @internal
   */
  completeOnLoad() {
    const href = globalThis.location?.href
    if (!href || !parseReturn(href)) return
    this.ready = this.completeSignIn(href)
    this.#completed = this.ready
    // A page that never reads `ready` must not trip the runtime's unhandled-rejection report:
    // the failure is still there for whoever does.
    this.ready.catch(() => {})
  }

  /**
   * Creates an account and signs in. Realms with `signup: invite` need an
   * invitation token. `locale` (such as `es` or `es-MX`) is the language to
   * use for the user; the server maps it silently to one the realm lists. A
   * browser also sends its `Accept-Language`, which is used when no `locale`
   * is given. `redirectTo` is where the page after the verification link may
   * send the user (it must be within the realm's `email.allowed_redirects`).
   *
   * A realm that requires verified addresses (`account.require_verified_email`)
   * creates the account but starts no session: this rejects with a
   * {@link VerificationRequiredError}, nothing is stored, and the user signs
   * in after following the link in the email they were sent.
   * @param {{ email: string, password: string, invitation?: string, locale?: string, redirectTo?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<Session>}
   */
  async signup({ email, password, invitation, locale, redirectTo }, opts) {
    /** @type {Record<string, string | boolean>} */
    const body = { email, password }
    if (this.client.cookies) body.cookie = true
    if (invitation) body.invitation = invitation
    if (locale) body.locale = locale
    if (redirectTo) body.redirect_to = redirectTo
    const { status, data } = await this.client.request({ method: 'POST', path: ['_auth', 'signup'], body, auth: false, ...opts })
    if (status === 202) {
      throw new VerificationRequiredError({ status, code: 'verification_required', message: 'the account was created: verify the email address, then sign in' })
    }
    return this.signedIn(data)
  }

  /**
   * Signs in with email and password.
   * @param {{ email: string, password: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<Session>}
   */
  async login({ email, password }, opts) {
    const body = this.client.cookies ? { email, password, cookie: true } : { email, password }
    const { data } = await this.client.request({ method: 'POST', path: ['_auth', 'login'], body, auth: false, ...opts })
    return this.signedIn(data)
  }

  /**
   * Starts signing in with a provider the realm lists (`google`, `microsoft` or `apple`): makes a PKCE
   * verifier, keeps it for the return, and sends the browser to the provider. The user comes back to
   * `redirectTo`, where {@link Auth.completeSignIn} finishes the sign-in. Resolves with the address it
   * sent the browser to (in a browser the page is already leaving).
   * @param {string} provider
   * @param {SignInWithOptions} [options]
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ authorizeUrl: string }>}
   */
  async signInWith(provider, options = {}, opts) {
    return this.#startOAuth(provider, 'signin', options, opts)
  }

  /**
   * Adds a provider to the signed-in user as another way to sign in. The same flow as
   * {@link Auth.signInWith}; {@link Auth.completeSignIn} then resolves with `{ linked: provider }`.
   * @param {string} provider
   * @param {SignInWithOptions} [options]
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ authorizeUrl: string }>}
   */
  async linkProvider(provider, options = {}, opts) {
    return this.#startOAuth(provider, 'link', options, opts)
  }

  /**
   * @param {string} provider
   * @param {'signin' | 'link'} intent
   * @param {SignInWithOptions} options
   * @param {RequestOptions} [opts]
   */
  async #startOAuth(provider, intent, options, opts) {
    const here = globalThis.location
    const redirectTo = options.redirectTo ?? (here ? here.origin + here.pathname : undefined)
    // Without a page of its own the realm's `sign_in.app_url` is where the flow ends (or the server refuses).
    const { verifier, challenge } = newPKCE()
    const body = compact({ redirect_to: redirectTo, code_challenge: challenge, intent, invitation: options.invitation, locale: options.locale })
    const { data } = await this.client.request({
      method: 'POST', path: ['_auth', 'oauth', provider, 'start'], body, auth: intent === 'link', ...opts,
    })
    savePending(this.#oauthStorage(), { verifier, provider, intent })
    const authorizeUrl = /** @type {string} */ (data.authorize_url)
    if (options.navigate) await options.navigate(authorizeUrl)
    else if (here) here.assign(authorizeUrl)
    return { authorizeUrl }
  }

  /** @returns {import('./oauth.js').OAuthStorage} */
  #oauthStorage() {
    return (this._oauthStorage ??= this.client.oauthStorage ?? defaultOAuthStorage())
  }

  /**
   * Finishes a sign-in with a provider on the page backd sent the user back to. It reads the outcome
   * from the address: a sign-in is traded for a session (stored by the client, like a login's) and
   * resolves with it, with `new_user` and, for a new user, the `profile` the provider gave; linking
   * resolves with `{ linked: provider }`; a failure rejects with the error the code means
   * (`account_exists` and `link_conflict` are a `ConflictError`, `signup_closed`, `email_not_verified`
   * and `signin_refused` a `ForbiddenError`, `cancelled` an `AuthenticationError`, …; `code` is the
   * code). Resolves with null on an ordinary visit that has nothing to finish, so it can be called on
   * every page load. In a browser it removes the outcome from the address bar. The client already does
   * this when it is created (see {@link Auth.ready}), so an app rarely calls it; a page that finds the
   * outcome gone from its address (`ready` took it) gets the same outcome when it calls this with no
   * address.
   * A page opened where the sign-in was not started (another device, browser or tab) has no PKCE
   * verifier to redeem the code with: that rejects with a `ValidationError` whose `code` is `wrong_device`.
   * @param {string | URL} [href]  The address to read; the page's own by default.
   * @param {RequestOptions} [opts]
   * @returns {Promise<ProviderSession | { linked: string } | null>}
   */
  async completeSignIn(href = globalThis.location?.href, opts) {
    if (arguments.length === 0 && this.#completed) return this.#completed
    const ret = parseReturn(href)
    if (!ret || !href) return null
    const storage = this.#oauthStorage()
    const pending = loadPending(storage)
    storage.remove()
    if (globalThis.history?.replaceState && globalThis.location && String(href) === globalThis.location.href) {
      globalThis.history.replaceState(null, '', withoutReturn(href))
    }
    if (ret.error) throw oauthError(ret.error, ret.provider ?? pending?.provider)
    if (ret.linked) return { linked: ret.linked }
    if (!pending) {
      throw new ValidationError({ status: 400, code: 'wrong_device', message: 'this page was not where the sign-in started: open the link on the device and tab where you started it' })
    }
    /** @type {Record<string, unknown>} */
    const body = { code: ret.code, code_verifier: pending.verifier }
    if (this.client.cookies) body.cookie = true
    const { data } = await this.client.request({ method: 'POST', path: ['_auth', 'token'], body, auth: false, ...opts })
    return /** @type {ProviderSession} */ (await this.signedIn(data))
  }

  /**
   * Removes one of the user's ways to sign in (`password` included). The last one can't be removed:
   * that rejects with a `ConflictError` whose `code` is `last_sign_in_method`.
   * @param {string} provider
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async unlinkProvider(provider, opts) {
    await this.client.request({ method: 'DELETE', path: ['_auth', 'identities', provider], reauth: true, ...opts })
  }

  /**
   * Signs in with the ID token a mobile app got from the platform sign-in (Sign in with Apple, Google
   * Sign-In, MSAL), with the raw `nonce` it started that sign-in with. For Apple, also the
   * `authorizationCode` its SDK gave. Rejects with `invalid_token` (a `AuthenticationError`) when the token
   * does not verify, and with `account_exists`, `signup_closed`, … as {@link Auth.completeSignIn} does.
   * @param {string} provider
   * @param {{ idToken: string, nonce: string, authorizationCode?: string, invitation?: string, locale?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<ProviderSession>}
   */
  async signInWithIdToken(provider, input, opts) {
    const { data } = await this.client.request({
      method: 'POST', path: ['_auth', 'oauth', provider, 'id-token'], body: this.#idTokenBody(input), auth: false, ...opts,
    })
    return /** @type {ProviderSession} */ (await this.signedIn(data))
  }

  /**
   * Adds a provider to the signed-in user with the ID token of the platform sign-in.
   * @param {string} provider
   * @param {{ idToken: string, nonce: string, authorizationCode?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ linked: string }>}
   */
  async linkProviderWithIdToken(provider, input, opts) {
    const { data } = await this.client.request({
      method: 'POST', path: ['_auth', 'oauth', provider, 'id-token'], body: { ...this.#idTokenBody(input), intent: 'link' }, ...opts,
    })
    return data
  }

  /** @param {{ idToken: string, nonce: string, authorizationCode?: string, invitation?: string, locale?: string }} input */
  #idTokenBody(input) {
    return compact({
      id_token: input.idToken, nonce: input.nonce, authorization_code: input.authorizationCode,
      invitation: input.invitation, locale: input.locale, cookie: this.client.cookies ? true : undefined,
    })
  }

  /**
   * Asks for the verification email again. Always resolves, whatever the
   * address is (unknown, disabled, already verified), so it can't be used to
   * find out who is registered. `redirectTo` is where the page after the link
   * may send the user (within the realm's `email.allowed_redirects`).
   * @param {{ email: string, redirectTo?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async resendVerification({ email, redirectTo }, opts) {
    await this.accepted(['_auth', 'verify-email', 'resend'], { email, redirect_to: redirectTo }, opts)
  }

  /**
   * Verifies an address with the token of the link in the email: for apps
   * that host their own page (`email.links` in `realm.yaml`). Starts no
   * session. An expired, used or unknown token rejects with a
   * `ValidationError` whose `code` is `invalid_token`.
   * @param {string} token
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async verifyEmail(token, opts) {
    await this.accepted(['_auth', 'verify-email'], { token }, opts)
  }

  /**
   * Asks for a password reset email. Always resolves, whatever the address is.
   * Rejects with a `RetryableError` once the realm's limits for reset
   * requests (per address and per client) are reached.
   * @param {{ email: string, redirectTo?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async requestPasswordReset({ email, redirectTo }, opts) {
    await this.accepted(['_auth', 'reset-password', 'request'], { email, redirect_to: redirectTo }, opts)
  }

  /**
   * Sets a new password with the token of a reset link. Ends every session of
   * the user, verifies their address and starts no session: log in afterwards.
   * A password the policy refuses rejects with a `ValidationError` and leaves
   * the token usable; a bad token has the `invalid_token` code.
   * @param {{ token: string, password: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async resetPassword({ token, password }, opts) {
    await this.accepted(['_auth', 'reset-password'], { token, password }, opts)
  }

  /**
   * Asks to change the signed-in user's email address (realms with
   * `account.allow_email_change`). Needs a fresh proof: call {@link Auth#reauth} first. Resolves
   * whether or not the new address is free; nothing changes until the link
   * sent to the new address is used (see {@link Auth#confirmEmailChange}).
   * @param {{ newEmail: string, redirectTo?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async requestEmailChange({ newEmail, redirectTo }, opts) {
    await this.client.request({ method: 'POST', path: ['_auth', 'email'], body: compact({ new_email: newEmail, redirect_to: redirectTo }), reauth: true, ...opts })
  }

  /**
   * Confirms an email change with the token sent to the new address: the
   * address changes and every session of the user ends.
   * @param {string} token
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async confirmEmailChange(token, opts) {
    await this.accepted(['_auth', 'confirm-email-change'], { token }, opts)
  }

  /**
   * Undoes an email change with the token sent to the old address: restores
   * it, ends every session and makes the password unusable until it is reset
   * (a reset email is sent to the restored address).
   * @param {string} token
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async revertEmailChange(token, opts) {
    await this.accepted(['_auth', 'revert-email-change'], { token }, opts)
  }

  /**
   * Accepts an invitation that was emailed (admin `invitations.send`), with
   * the token of its link: creates the account for the invited address,
   * already verified, and starts no session. `locale` is the user's language.
   * @param {{ token: string, password: string, locale?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async acceptInvitation({ token, password, locale }, opts) {
    await this.accepted(['_auth', 'accept-invitation'], { token, password, locale }, opts)
  }

  /**
   * A call that needs no session and answers with no body.
   * @internal
   * @param {string[]} path
   * @param {Record<string, unknown>} body
   * @param {RequestOptions} [opts]
   */
  async accepted(path, body, opts) {
    await this.client.request({ method: 'POST', path, body: compact(body), auth: false, ...opts })
  }

  /**
   * Ends the current session. The stored token is removed even if the
   * server can't be reached.
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async logout(opts) {
    try {
      await this.client.request({ method: 'POST', path: ['_auth', 'logout'], expire: false, ...opts })
    } catch (err) {
      // A session the server no longer knows is logged out already.
      if (!(err instanceof AuthenticationError)) throw err
    } finally {
      await this.signedOut('SIGNED_OUT')
    }
  }

  /**
   * Ends every session of the user, on every device.
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async logoutAll(opts) {
    await this.client.request({ method: 'POST', path: ['_auth', 'logout-all'], ...opts })
    await this.signedOut('SIGNED_OUT')
  }

  /**
   * The signed-in user, with their sign-in methods in `identities`.
   * @param {RequestOptions} [opts]
   * @returns {Promise<Me>}
   */
  async me(opts) {
    return (await this.client.request({ method: 'GET', path: ['_auth', 'me'], ...opts })).data
  }

  /**
   * Changes the signed-in user's own settings: today their language, which
   * must be one the realm lists (case is ignored). Anything else is a
   * `ValidationError` with code `invalid_locale` and the allowed languages in
   * `details`.
   * @param {{ locale?: string }} changes
   * @param {RequestOptions} [opts]
   * @returns {Promise<User>}
   */
  async updateMe(changes, opts) {
    return (await this.client.request({ method: 'PATCH', path: ['_auth', 'me'], body: changes, ...opts })).data
  }

  /**
   * Deletes the signed-in user's account: it is **deactivated** (disabled, its
   * sessions end) and all data is kept. Erasing a user's data is an
   * administrator's action (`admin.users.delete`).
   * Needs a fresh proof: call {@link Auth#reauth} first (a `reauth_required` error otherwise).
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async deleteAccount(opts) {
    await this.client.request({ method: 'DELETE', path: ['_auth', 'me'], reauth: true, ...opts })
    await this.signedOut('SIGNED_OUT')
  }

  /**
   * Changes the password. This session stays valid; all others end. Needs a fresh proof:
   * call {@link Auth#reauth} first.
   * @param {{ newPassword: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async changePassword({ newPassword }, opts) {
    await this.client.request({
      method: 'POST',
      path: ['_auth', 'password'],
      body: { new_password: newPassword },
      reauth: true,
      ...opts,
    })
  }

  /**
   * Proves who the signed-in user is again, before a sensitive action (changing the password or
   * the address, deleting the account, unlinking a sign-in method, managing the second factor,
   * creating an API key as an administrator). `method` is the strongest proof the user has (see
   * `reauth_methods` of {@link Auth#me}): `totp` or `recovery_code` with a second factor,
   * `password`, or `email_code` after {@link Auth#requestReauthCode}. The client keeps the proof
   * and sends it with the sensitive requests while it is good (the realm's `reauth.ttl`, 5 minutes
   * by default), so one prompt covers a settings page. A wrong proof is a `AuthenticationError`
   * (`invalid_credentials`) or a `ValidationError` (`invalid_code`) and counts against the same
   * limits as signing in.
   * @param {{ method: 'totp' | 'recovery_code' | 'password' | 'email_code', code?: string, password?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ reauthToken: string, expiresAt: string }>}
   */
  async reauth(input, opts) {
    const { data } = await this.client.request({
      method: 'POST', path: ['_auth', 'reauth'], body: compact({ method: input.method, code: input.code, password: input.password }), ...opts,
    })
    this.client.proof = { token: data.reauth_token, expires: Date.parse(data.expires_at) }
    return { reauthToken: data.reauth_token, expiresAt: data.expires_at }
  }

  /**
   * Whether the client holds a proof that is still good (see {@link Auth#reauth}).
   * @returns {boolean}
   */
  hasFreshProof() {
    return Boolean(this.client.proof && this.client.proof.expires > Date.now())
  }

  /**
   * Emails a code to a user who has no password and no second factor, to give to
   * {@link Auth#reauth} with `method: 'email_code'`.
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async requestReauthCode(opts) {
    await this.client.request({ method: 'POST', path: ['_auth', 'reauth', 'email-code'], ...opts })
  }

  /**
   * The user's active sessions, newest first.
   * @param {RequestOptions} [opts]
   * @returns {Promise<SessionInfo[]>}
   */
  async sessions(opts) {
    return (await this.client.request({ method: 'GET', path: ['_auth', 'sessions'], ...opts })).data.items
  }

  /**
   * Ends one of the user's sessions, for example a lost device.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async revokeSession(id, opts) {
    await this.client.request({ method: 'DELETE', path: ['_auth', 'sessions', id], ...opts })
  }

  /**
   * The stored session token, if any. Always null with `cookies: true`: the
   * token is in a cookie that scripts can't read.
   * @returns {Promise<string | null>}
   */
  async token() {
    const token = (await this.client.storage.get()) ?? null
    return token === COOKIE_SESSION ? null : token
  }

  /**
   * Whether this client believes it is signed in: it holds a token, or (with
   * `cookies: true`) it signed in and hasn't signed out or been refused since.
   * A page that has just loaded knows nothing: ask the server with {@link Auth#me}.
   * @returns {Promise<boolean>}
   */
  async hasSession() {
    return Boolean(await this.client.storage.get())
  }

  /**
   * Calls listener on sign-in, sign-out and session expiry.
   * @param {AuthListener} listener
   * @returns {() => void} Stops listening.
   */
  onAuthChange(listener) {
    this.listeners.add(listener)
    return () => this.listeners.delete(listener)
  }

  /**
   * @internal
   * @param {Session} session
   * @returns {Promise<Session>}
   */
  async signedIn(session) {
    this.client.proof = undefined
    await this.client.storage.set(session.token ?? COOKIE_SESSION)
    this.emit('SIGNED_IN', session)
    return session
  }

  /**
   * @internal
   * @param {AuthEvent} event
   */
  async signedOut(event) {
    this.client.proof = undefined
    await this.client.storage.remove()
    this.emit(event, null)
  }

  /**
   * Called by the client when the server refuses the stored token.
   * @internal
   */
  async _expired() {
    await this.signedOut('SESSION_EXPIRED')
  }

  /**
   * @internal
   * @param {AuthEvent} event
   * @param {Session | null} session
   */
  emit(event, session) {
    for (const l of this.listeners) {
      try {
        l(event, session)
      } catch (err) {
        // A failing listener must not break the client or other listeners.
        console.error('backd: an onAuthChange listener failed:', err)
      }
    }
  }
}
