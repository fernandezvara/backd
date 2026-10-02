import { AuthenticationError, VerificationRequiredError } from './errors.js'

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
 * A new session.
 * @typedef {object} Session
 * @property {string} token       Session token (`bds_…`); already stored by the client.
 * @property {'Bearer'} token_type
 * @property {string} session_id
 * @property {string} expires_at  RFC3339; pushed forward as the session is used.
 * @property {User} user
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
  /** @param {Client} client */
  constructor(client) {
    /** @private */
    this.client = client
    /** @private @type {Set<AuthListener>} */
    this.listeners = new Set()
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
    /** @type {Record<string, string>} */
    const body = { email, password }
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
    const { data } = await this.client.request({ method: 'POST', path: ['_auth', 'login'], body: { email, password }, auth: false, ...opts })
    return this.signedIn(data)
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
   * `account.allow_email_change`). Needs the current password. Resolves
   * whether or not the new address is free; nothing changes until the link
   * sent to the new address is used (see {@link Auth#confirmEmailChange}).
   * @param {{ newEmail: string, password: string, redirectTo?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async requestEmailChange({ newEmail, password, redirectTo }, opts) {
    await this.client.request({ method: 'POST', path: ['_auth', 'email'], body: compact({ new_email: newEmail, password, redirect_to: redirectTo }), ...opts })
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
   * @private
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
   * The signed-in user.
   * @param {RequestOptions} [opts]
   * @returns {Promise<User>}
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
   * @param {{ password: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async deleteAccount({ password }, opts) {
    await this.client.request({ method: 'DELETE', path: ['_auth', 'me'], body: { password }, ...opts })
    await this.signedOut('SIGNED_OUT')
  }

  /**
   * Changes the password. This session stays valid; all others end.
   * @param {{ currentPassword: string, newPassword: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async changePassword({ currentPassword, newPassword }, opts) {
    await this.client.request({
      method: 'POST',
      path: ['_auth', 'password'],
      body: { current_password: currentPassword, new_password: newPassword },
      ...opts,
    })
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
   * The stored session token, if any.
   * @returns {Promise<string | null>}
   */
  async token() {
    return (await this.client.storage.get()) ?? null
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
   * @private
   * @param {Session} session
   * @returns {Promise<Session>}
   */
  async signedIn(session) {
    await this.client.storage.set(session.token)
    this.emit('SIGNED_IN', session)
    return session
  }

  /**
   * @private
   * @param {AuthEvent} event
   */
  async signedOut(event) {
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
   * @private
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
