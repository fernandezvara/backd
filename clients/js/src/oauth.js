import { BackdError, errorClassFor } from './errors.js'
import { Sha256 } from './sha256.js'

/**
 * Where the PKCE verifier waits while the user is at the provider: it must survive the
 * navigation away and back, so a tab's `sessionStorage` by default (memory where there is none,
 * which is enough for an app that handles the redirect without leaving).
 * @typedef {object} OAuthStorage
 * @property {() => string | null | undefined} get
 * @property {(value: string) => void} set
 * @property {() => void} remove
 */

const PENDING_KEY = 'backd.oauth.pending'
const PENDING_LIFE_MS = 10 * 60 * 1000 // the server's state lives ten minutes

/**
 * @param {string} [key]
 * @returns {OAuthStorage}
 */
export function defaultOAuthStorage(key = PENDING_KEY) {
  let ss
  try {
    ss = globalThis.sessionStorage
  } catch {
    // Some browsers refuse access when storage is blocked.
  }
  if (ss) {
    const s = ss
    return { get: () => s.getItem(key), set: (v) => s.setItem(key, v), remove: () => s.removeItem(key) }
  }
  /** @type {string | null} */
  let held = null
  return { get: () => held, set: (v) => (held = v), remove: () => (held = null) }
}

/** @param {Uint8Array} bytes */
function base64url(bytes) {
  let s = ''
  for (const b of bytes) s += String.fromCharCode(b)
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
}

/**
 * A PKCE verifier (256 random bits) and its S256 challenge.
 * @returns {{ verifier: string, challenge: string }}
 */
export function newPKCE() {
  const crypto = globalThis.crypto
  if (!crypto?.getRandomValues) throw new Error('backd-js: this runtime has no crypto.getRandomValues, needed for PKCE')
  const verifier = base64url(crypto.getRandomValues(new Uint8Array(32)))
  return { verifier, challenge: challengeOf(verifier) }
}

/** The S256 challenge of a verifier. @param {string} verifier */
export function challengeOf(verifier) {
  const hex = new Sha256().update(new TextEncoder().encode(verifier)).hex()
  return base64url(Uint8Array.from(hex.match(/../g) ?? [], (h) => parseInt(h, 16)))
}

/**
 * @typedef {object} Pending
 * @property {string} verifier
 * @property {string} provider
 * @property {'signin' | 'link'} intent
 * @property {number} at
 */

/**
 * @param {OAuthStorage} storage
 * @param {Omit<Pending, 'at'>} p
 */
export function savePending(storage, p) {
  storage.set(JSON.stringify({ ...p, at: Date.now() }))
}

/**
 * The attempt this page started, or null when none is waiting (or it is too old to work).
 * @param {OAuthStorage} storage
 * @returns {Pending | null}
 */
export function loadPending(storage) {
  try {
    const p = JSON.parse(storage.get() ?? 'null')
    if (p && typeof p.verifier === 'string' && Date.now() - p.at < PENDING_LIFE_MS) return p
  } catch {
    // Unreadable: as if there were none.
  }
  return null
}

/**
 * What backd put in the address it sent the browser back to.
 * @typedef {object} OAuthReturn
 * @property {string} [code]       A one-time login code.
 * @property {string} [error]      Why the sign-in didn't work.
 * @property {string} [linked]     The provider that was linked to the signed-in user.
 * @property {string} [provider]   With `account_exists`: the provider that was tried.
 */

/**
 * Reads the outcome of the redirect flow from the address of the page the user returned to;
 * null when it carries none (an ordinary visit).
 * @param {string | URL | undefined} href
 * @returns {OAuthReturn | null}
 */
export function parseReturn(href) {
  if (!href) return null
  let u
  try {
    u = new URL(String(href))
  } catch {
    return null
  }
  const q = u.searchParams
  /** @type {OAuthReturn} */
  const out = {}
  for (const k of /** @type {const} */ (['code', 'error', 'linked', 'provider'])) {
    const v = q.get(k)
    if (v) out[k] = v
  }
  return out.code || out.error || out.linked ? out : null
}

/**
 * `href` without the parameters the flow added, to leave in the address bar.
 * @param {string | URL} href
 */
export function withoutReturn(href) {
  const u = new URL(String(href))
  for (const k of ['code', 'error', 'linked', 'provider']) u.searchParams.delete(k)
  return u.toString()
}

/** @type {Record<string, { status: number, message: string }>} */
const ERRORS = {
  cancelled: { status: 401, message: 'the sign-in was cancelled at the provider' },
  account_exists: { status: 409, message: 'an account with that address exists: sign in another way and link this provider from there' },
  signup_closed: { status: 403, message: 'sign-up is not open in this realm' },
  invitation_invalid: { status: 400, message: 'sign-up needs a valid invitation' },
  link_conflict: { status: 409, message: 'that provider account can not be linked to this user' },
  email_not_verified: { status: 403, message: 'the address of this account is not verified' },
  email_required: { status: 400, message: 'the provider gave no email address' },
  signin_refused: { status: 403, message: 'this account can not sign in' },
  provider_error: { status: 502, message: 'the provider failed or answered something backd can not accept' },
}

/**
 * The error for an `?error=` code of the redirect flow, as the same classes the rest of the client
 * throws: `account_exists` and `link_conflict` are a `ConflictError`, `signup_closed`,
 * `email_not_verified` and `signin_refused` a `ForbiddenError`, `cancelled` an
 * `AuthenticationError`, and so on; `code` is the error code, and `details` names the provider.
 * @param {string} code
 * @param {string} [provider]
 * @returns {BackdError}
 */
export function oauthError(code, provider) {
  const known = ERRORS[code] ?? ERRORS.provider_error
  const Class = errorClassFor(known.status)
  return new Class({
    status: known.status,
    code: ERRORS[code] ? code : 'provider_error',
    message: known.message,
    details: provider ? [{ path: 'provider', reason: provider }] : [],
  })
}
