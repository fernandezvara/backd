import { AuthenticationError, SecondStepRequiredError, ValidationError, VerificationRequiredError } from './errors.js'
/** @typedef {import('./errors.js').ConsentRequiredError} ConsentRequiredError */
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
 * @typedef {User & { identities: Identity[], reauth_methods: Array<'totp' | 'recovery_code' | 'password' | 'email_code'>, sign_in_methods: string[] }} Me
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
 * A sign-in that needs a code of the user's second factor before it gives a session: send it
 * with {@link Auth#verifyMfa}.
 * @typedef {object} MfaChallenge
 * @property {true} mfaRequired
 * @property {string} challenge
 * @property {Array<'totp' | 'recovery_code'>} methods
 */

/**
 * A sign-in of a user whose role requires a second factor and who has none: set one up with
 * `auth.mfa.setup({ challenge })` and `auth.mfa.confirm({ challenge, setupId, code })`, which
 * gives the session.
 * @typedef {object} EnrollmentChallenge
 * @property {true} enrollmentRequired
 * @property {string} challenge
 */

/**
 * A sign-in that is done but has consents to accept first (a required one never accepted, or a new
 * version that asks for re-acceptance): show their texts (`client.consents.text`) and send the
 * acceptance with {@link Auth#acceptConsents}.
 * @typedef {object} ConsentChallenge
 * @property {true} consentRequired
 * @property {string} challenge
 * @property {Array<{ name: string, version: string }>} consents
 */

/**
 * What a first factor leads to: a session, or a second step.
 * @typedef {Session | MfaChallenge | EnrollmentChallenge | ConsentChallenge} SignInResult
 */

/**
 * Options of the redirect sign-in.
 * @typedef {object} SignInWithOptions
 * @property {string} [redirectTo]    Where backd sends the browser back to (it must be within the realm's
 *   `sign_in.allowed_redirects`); this page, without its query, by default, and outside a browser
 *   the realm's `sign_in.app_url`.
 * @property {string} [invitation]    An invitation token, for realms with `signup: invite`.
 * @property {string} [locale]
 * @property {Record<string, string>} [consents]  Consents accepted for the account the sign-in may make
 *   (name to version); a new account made without them is asked at its first sign-in.
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

/** The signed-in user's second factor (`client.auth.mfa`). Changes need a fresh proof: see {@link Auth#reauth}. */
class Mfa {
  /** @param {Auth} auth */
  constructor(auth) {
    this.auth = auth
  }

  /**
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ available: boolean, enabled: boolean, methods: string[], recovery_codes_left: number, required: boolean }>}
   */
  async status(opts) {
    return (await this.auth.client.request({ method: 'GET', path: ['_auth', 'mfa'], ...opts })).data
  }

  /**
   * Starts setting an authenticator app up: the secret, the `otpauth_uri` and `qr` (a PNG data URI).
   * With the `challenge` of an {@link EnrollmentChallenge} there is no session yet.
   * @param {{ challenge?: string }} [input]
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ setup_id: string, secret: string, otpauth_uri: string, qr: string, expires_at: string }>}
   */
  async setup(input = {}, opts) {
    const { challenge } = input
    return (await this.auth.client.request({
      method: 'POST', path: ['_auth', 'mfa', 'totp', 'setup'], body: challenge ? { challenge } : undefined, reauth: !challenge, auth: !challenge, ...opts,
    })).data
  }

  /**
   * Turns it on with the first code of the app. Resolves with the `recovery_codes`, shown once; with
   * a `challenge` it also ends the sign-in and the client holds the session (`session` is it).
   * @param {{ setupId: string, code: string, challenge?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ recovery_codes: string[], session?: Session }>}
   */
  async confirm({ setupId, code, challenge }, opts) {
    /** @type {Record<string, unknown>} */
    const body = { setup_id: setupId, code }
    if (challenge) {
      body.challenge = challenge
      if (this.auth.client.cookies) body.cookie = true
    }
    const { data } = await this.auth.client.request({
      method: 'POST', path: ['_auth', 'mfa', 'totp', 'confirm'], body, reauth: !challenge, auth: !challenge, ...opts,
    })
    if (data.token || data.session_id) {
      const { recovery_codes: codes, ...session } = data
      return { recovery_codes: codes, session: /** @type {Session} */ (await this.auth.signedIn(session)) }
    }
    return { recovery_codes: data.recovery_codes }
  }

  /**
   * Turns it off (not for a role that requires it: `mfa_required_by_role`).
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async disable(opts) {
    await this.auth.client.request({ method: 'DELETE', path: ['_auth', 'mfa', 'totp'], reauth: true, ...opts })
  }

  /**
   * Replaces the recovery codes; the new ones are shown once.
   * @param {RequestOptions} [opts]
   * @returns {Promise<string[]>}
   */
  async regenerateRecoveryCodes(opts) {
    return (await this.auth.client.request({ method: 'POST', path: ['_auth', 'mfa', 'recovery-codes'], reauth: true, ...opts })).data.recovery_codes
  }
}

/** Sign-in by a link or a code emailed to the user (`client.auth.magicLink`). */
class MagicLink {
  /** @param {Auth} auth */
  constructor(auth) {
    this.auth = auth
  }

  /**
   * Asks for a message with a link and a 6-digit code. Always resolves, whatever the address is. The
   * link ends at the current page (or `redirectTo`), where the client finishes the sign-in when it is
   * created (see {@link Auth#ready}); the code is typed in this app with {@link MagicLink#verifyCode}.
   * The PKCE verifier waits in the tab's storage for both.
   * @param {string} email
   * `consents` are accepted for the account a new address gets (a new account made without them is asked
   * at its first sign-in).
   * @param {{ redirectTo?: string, locale?: string, consents?: Record<string, string> }} [options]
   * @param {RequestOptions} [opts]
   * @returns {Promise<{ requestId: string }>}
   */
  async request(email, options = {}, opts) {
    const here = globalThis.location
    const redirectTo = options.redirectTo ?? (here ? here.origin + here.pathname : undefined)
    const { verifier, challenge } = newPKCE()
    const { data } = await this.auth.client.request({
      method: 'POST', path: ['_auth', 'magic-link'], auth: false, ...opts,
      body: compact({ email, redirect_to: redirectTo, code_challenge: challenge, locale: options.locale, consents: options.consents }),
    })
    savePending(this.auth.oauthStorage(), { verifier, provider: 'magic-link', intent: 'signin', requestId: data.request_id })
    return { requestId: data.request_id }
  }

  /**
   * Signs in with the code of the message, in the app that asked. The answer is the session or a
   * second step, like {@link Auth#login}'s. A wrong code is `invalid_code` (five end the request);
   * a request that is unknown, used or expired is `invalid_magic_link`.
   * @param {string} code
   * @param {RequestOptions} [opts]
   * @returns {Promise<ProviderSession | MfaChallenge | EnrollmentChallenge | ConsentChallenge>}
   */
  async verifyCode(code, opts) {
    const storage = this.auth.oauthStorage()
    const pending = loadPending(storage)
    if (!pending || !pending.requestId) {
      throw new ValidationError({ status: 400, code: 'no_request', message: 'ask for a link first, in this tab: request(email)' })
    }
    /** @type {Record<string, unknown>} */
    const body = { request_id: pending.requestId, code: code.trim(), code_verifier: pending.verifier }
    if (this.auth.client.cookies) body.cookie = true
    const { data } = await this.auth.client.request({ method: 'POST', path: ['_auth', 'magic-link', 'verify'], body, auth: false, ...opts })
    storage.remove()
    return /** @type {any} */ (await this.auth.step(data))
  }
}

/** Sign-up, login and session management: `client.auth`. */
export class Auth {
  /** @type {Promise<ProviderSession | MfaChallenge | EnrollmentChallenge | ConsentChallenge | { linked: string } | null> | undefined} */
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
     * @type {Promise<ProviderSession | MfaChallenge | EnrollmentChallenge | ConsentChallenge | { linked: string } | null>}
     */
    this.ready = Promise.resolve(null)
    /** The signed-in user's second factor: `client.auth.mfa`. */
    this.mfa = new Mfa(this)
    /** Sign-in by an emailed link and code: `client.auth.magicLink`. */
    this.magicLink = new MagicLink(this)
    /** Setting and removing the password of an account that may sign in another way. */
    this.password = {
      /** @param {{ newPassword: string }} input @param {RequestOptions} [opts] */
      set: (input, opts) => this.changePassword(input, opts),
      /** @param {RequestOptions} [opts] */
      remove: async (opts) => {
        await this.client.request({ method: 'DELETE', path: ['_auth', 'password'], reauth: true, ...opts })
      },
    }
  }

  /**
   * What a first factor answered: the session (stored), or the second step the user has to take.
   * @internal
   * @param {any} data
   * @returns {Promise<SignInResult>}
   */
  async step(data) {
    if (data?.mfa_required) return { mfaRequired: true, challenge: data.challenge, methods: data.methods }
    if (data?.mfa_enrollment_required) return { enrollmentRequired: true, challenge: data.challenge }
    if (data?.consent_required) return { consentRequired: true, challenge: data.challenge, consents: data.consents }
    return this.signedIn(data)
  }

  /**
   * Finishes a sign-in that answered with an {@link MfaChallenge}: the code of the user's
   * authenticator app (`totp`) or a recovery code (`recovery_code`). Resolves with the session (a
   * provider's also has `new_user`, a recovery code `recovery_codes_left`). Rejects with
   * `invalid_code` (a `ValidationError`; `details` has the attempts left), `invalid_challenge`
   * after five wrong ones or five minutes, and `mfa_locked` (a `RetryableError`, 429) after ten wrong
   * codes in an hour.
   * @param {string} challenge
   * @param {'totp' | 'recovery_code'} method
   * @param {string} code
   * @param {RequestOptions} [opts]
   * @returns {Promise<Session & { new_user?: boolean, profile?: Profile, recovery_codes_left?: number }>}
   */
  async verifyMfa(challenge, method, code, opts) {
    /** @type {Record<string, unknown>} */
    const body = { challenge, method, code }
    if (this.client.cookies) body.cookie = true
    const { data } = await this.client.request({ method: 'POST', path: ['_auth', 'mfa', 'verify'], body, auth: false, ...opts })
    return /** @type {any} */ (await this.signedIn(data))
  }

  /**
   * Finishes a sign-in that answered with a {@link ConsentChallenge}: the consents the user accepted,
   * as `{ name: version }`, with every one the challenge named at its version. Resolves with the
   * session (a provider's also has `new_user`). Rejects with a {@link ConsentRequiredError} when
   * one is missing (the challenge stays valid) and `invalid_challenge` after five minutes.
   * @param {string} challenge
   * @param {Record<string, string>} consents
   * @param {RequestOptions} [opts]
   * @returns {Promise<Session & { new_user?: boolean, profile?: Profile }>}
   */
  async acceptConsents(challenge, consents, opts) {
    /** @type {Record<string, unknown>} */
    const body = { challenge, consents }
    if (this.client.cookies) body.cookie = true
    const { data } = await this.client.request({ method: 'POST', path: ['_auth', 'consents', 'accept'], body, auth: false, ...opts })
    return /** @type {any} */ (await this.signedIn(data))
  }

  /**
   * Runs a sensitive action, asking the user for a fresh proof when the server wants one: `ask`
   * gets the proofs the user can give (`totp`, `recovery_code`, `password` or `email_code`) and
   * resolves with the one to use, as {@link Auth#reauth} takes it; the action is then tried once more.
   * @template T
   * @param {(methods: string[]) => Promise<{ method: 'totp' | 'recovery_code' | 'password' | 'email_code', code?: string, password?: string }>} ask
   * @param {() => Promise<T>} action
   * @returns {Promise<T>}
   */
  async withProof(ask, action) {
    try {
      return await action()
    } catch (err) {
      if (!(err instanceof AuthenticationError) || err.code !== 'reauth_required') throw err
    }
    const me = await this.me()
    await this.reauth(await ask(me.reauth_methods ?? []))
    return action()
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
   * A user whose role requires a second factor has no session yet: this rejects with a
   * {@link SecondStepRequiredError} (`mfa_enrollment_required`) carrying the challenge.
   * `consents` are the consents accepted, as the consent's name and the version shown (`{ terms: '2026-10' }`):
   * every required one of the realm must be there at its current version, or this rejects with a
   * {@link ConsentRequiredError} that lists them.
   * @param {{ email: string, password: string, invitation?: string, locale?: string, redirectTo?: string, consents?: Record<string, string> }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<Session>}
   */
  async signup({ email, password, invitation, locale, redirectTo, consents }, opts) {
    /** @type {Record<string, unknown>} */
    const body = { email, password }
    if (consents) body.consents = consents
    if (this.client.cookies) body.cookie = true
    if (invitation) body.invitation = invitation
    if (locale) body.locale = locale
    if (redirectTo) body.redirect_to = redirectTo
    const { status, data } = await this.client.request({ method: 'POST', path: ['_auth', 'signup'], body, auth: false, ...opts })
    if (status === 202) {
      throw new VerificationRequiredError({ status, code: 'verification_required', message: 'the account was created: verify the email address, then sign in' })
    }
    return this.session(await this.step(data))
  }

  /**
   * Turns a step into the session a method promises, or throws {@link SecondStepRequiredError}.
   * @internal
   * @param {SignInResult} result
   * @returns {Session}
   */
  session(result) {
    if ('consentRequired' in result) {
      throw new SecondStepRequiredError({
        status: 200, code: 'consent_required', challenge: result.challenge, consents: result.consents,
        message: 'consents must be accepted: show client.consents.text(name, version) and call auth.acceptConsents(challenge, consents)',
      })
    }
    if ('challenge' in result) {
      const enroll = 'enrollmentRequired' in result
      throw new SecondStepRequiredError({
        status: 200, code: enroll ? 'mfa_enrollment_required' : 'mfa_required', challenge: result.challenge,
        methods: enroll ? [] : result.methods,
        message: enroll ? 'this account must set up a second factor: auth.mfa.setup({ challenge })' : 'a code of the second factor is needed: auth.verifyMfa(challenge, method, code)',
      })
    }
    return result
  }

  /**
   * Signs in with email and password. The answer is the session, or, for a user with a second
   * factor, an {@link MfaChallenge} to finish with {@link Auth#verifyMfa}, or an
   * {@link EnrollmentChallenge}: check `'challenge' in result`. Nothing is stored until a session.
   * @param {{ email: string, password: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<SignInResult>}
   */
  async signIn({ email, password }, opts) {
    const body = this.client.cookies ? { email, password, cookie: true } : { email, password }
    const { data } = await this.client.request({ method: 'POST', path: ['_auth', 'login'], body, auth: false, ...opts })
    return this.step(data)
  }

  /**
   * Signs in with email and password and promises a session: a user who needs a second step makes
   * this reject with a {@link SecondStepRequiredError} (use {@link Auth#signIn} to get the step as a value).
   * @param {{ email: string, password: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<Session>}
   */
  async login(input, opts) {
    return this.session(await this.signIn(input, opts))
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
    const body = compact({ redirect_to: redirectTo, code_challenge: challenge, intent, invitation: options.invitation, locale: options.locale, consents: options.consents })
    const { data } = await this.client.request({
      method: 'POST', path: ['_auth', 'oauth', provider, 'start'], body, auth: intent === 'link', ...opts,
    })
    savePending(this.oauthStorage(), { verifier, provider, intent })
    const authorizeUrl = /** @type {string} */ (data.authorize_url)
    if (options.navigate) await options.navigate(authorizeUrl)
    else if (here) here.assign(authorizeUrl)
    return { authorizeUrl }
  }

  /**
   * @internal
   * @returns {import('./oauth.js').OAuthStorage}
   */
  oauthStorage() {
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
   * @returns {Promise<ProviderSession | MfaChallenge | EnrollmentChallenge | ConsentChallenge | { linked: string } | null>}
   */
  async completeSignIn(href = globalThis.location?.href, opts) {
    if (arguments.length === 0 && this.#completed) return this.#completed
    const ret = parseReturn(href)
    if (!ret || !href) return null
    const storage = this.oauthStorage()
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
    return /** @type {any} */ (await this.step(data))
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
   * @param {{ idToken: string, nonce: string, authorizationCode?: string, invitation?: string, locale?: string, consents?: Record<string, string> }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<ProviderSession | MfaChallenge | EnrollmentChallenge | ConsentChallenge>}
   */
  async signInWithIdToken(provider, input, opts) {
    const { data } = await this.client.request({
      method: 'POST', path: ['_auth', 'oauth', provider, 'id-token'], body: this.#idTokenBody(input), auth: false, ...opts,
    })
    return /** @type {any} */ (await this.step(data))
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

  /** @param {{ idToken: string, nonce: string, authorizationCode?: string, invitation?: string, locale?: string, consents?: Record<string, string> }} input */
  #idTokenBody(input) {
    return compact({
      id_token: input.idToken, nonce: input.nonce, authorization_code: input.authorizationCode,
      invitation: input.invitation, locale: input.locale, consents: input.consents, cookie: this.client.cookies ? true : undefined,
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
    this.client.clearCache() // what was read as someone else (or no one) isn't theirs
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
    this.client.clearCache()
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
