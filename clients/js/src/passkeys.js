import { BackdError } from './errors.js'

/** @typedef {import('./client.js').RequestOptions} RequestOptions */

/**
 * A passkey of the signed-in user. Never the key material.
 * @typedef {object} Passkey
 * @property {string} id
 * @property {string} name
 * @property {string} created_at
 * @property {string | null} last_used_at
 * @property {string[]} transports
 * @property {boolean} backup_eligible  The passkey syncs between the user's devices.
 * @property {boolean} backup_state     It has been synced.
 */

/**
 * What the browser can do with passkeys.
 * @typedef {object} PasskeySupport
 * @property {boolean} available    The browser can make and use passkeys (`PublicKeyCredential` exists).
 * @property {boolean} conditional  It can offer them in the sign-in field's autofill (conditional UI).
 */

/**
 * The ceremony did not happen: the browser has no passkeys (`unsupported`), the user dismissed the
 * prompt or it timed out (`cancelled`), or the authenticator refused (`failed`; `cause` is the
 * browser's error). It is not an answer of the server: nothing was sent to it.
 */
export class PasskeyError extends BackdError {
  /**
   * @param {'unsupported' | 'cancelled' | 'failed'} code
   * @param {string} message
   * @param {unknown} [cause]
   */
  constructor(code, message, cause) {
    super({ status: 0, code: 'passkey_' + code, message, cause })
  }
}

const B64 = /** @type {const} */ ('ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_')

/**
 * @param {ArrayBuffer | ArrayBufferView} buf
 * @returns {string} base64url without padding
 */
export function toBase64url(buf) {
  const bytes = ArrayBuffer.isView(buf) ? new Uint8Array(buf.buffer, buf.byteOffset, buf.byteLength) : new Uint8Array(buf)
  let out = ''
  for (let i = 0; i < bytes.length; i += 3) {
    const n = (bytes[i] << 16) | ((bytes[i + 1] ?? 0) << 8) | (bytes[i + 2] ?? 0)
    out += B64[(n >> 18) & 63] + B64[(n >> 12) & 63]
    if (i + 1 < bytes.length) out += B64[(n >> 6) & 63]
    if (i + 2 < bytes.length) out += B64[n & 63]
  }
  return out
}

/**
 * @param {string} s base64url, with or without padding
 * @returns {ArrayBuffer}
 */
export function fromBase64url(s) {
  const clean = s.replace(/=+$/, '')
  const out = new Uint8Array(Math.floor((clean.length * 6) / 8))
  let acc = 0
  let bits = 0
  let o = 0
  for (const ch of clean) {
    const v = B64.indexOf(/** @type {any} */ (ch))
    if (v < 0) throw new TypeError('not base64url')
    acc = (acc << 6) | v
    bits += 6
    if (bits >= 8) {
      bits -= 8
      out[o++] = (acc >> bits) & 255
    }
  }
  return out.buffer
}

/** @param {any} list @returns {any[] | undefined} */
function withIds(list) {
  return Array.isArray(list) ? list.map((d) => ({ ...d, id: fromBase64url(d.id) })) : undefined
}

/**
 * The options the server sent (the JSON form of the WebAuthn standard) as what `navigator.credentials`
 * takes: the platform's own `parse…OptionsFromJSON` when it has one, the same conversion otherwise.
 * @param {'create' | 'get'} kind
 * @param {any} json  The `publicKey` member.
 * @returns {any}
 */
export function parseOptions(kind, json) {
  const PKC = /** @type {any} */ (globalThis).PublicKeyCredential
  const native = kind === 'create' ? PKC?.parseCreationOptionsFromJSON : PKC?.parseRequestOptionsFromJSON
  if (typeof native === 'function') return native.call(PKC, json)
  const out = { ...json, challenge: fromBase64url(json.challenge) }
  if (kind === 'create') {
    out.user = { ...json.user, id: fromBase64url(json.user.id) }
    if (json.excludeCredentials) out.excludeCredentials = withIds(json.excludeCredentials)
  } else if (json.allowCredentials) {
    out.allowCredentials = withIds(json.allowCredentials)
  }
  return out
}

/**
 * The credential the browser made, in the JSON form the server takes: its own `toJSON()` when it has
 * one, the same conversion otherwise.
 * @param {any} cred  A `PublicKeyCredential`.
 * @returns {Record<string, unknown>}
 */
export function credentialToJSON(cred) {
  if (typeof cred.toJSON === 'function') return cred.toJSON()
  const r = cred.response
  /** @type {Record<string, unknown>} */
  const response = { clientDataJSON: toBase64url(r.clientDataJSON) }
  if (r.attestationObject) {
    response.attestationObject = toBase64url(r.attestationObject)
    if (typeof r.getTransports === 'function') response.transports = r.getTransports()
    if (typeof r.getPublicKeyAlgorithm === 'function') response.publicKeyAlgorithm = r.getPublicKeyAlgorithm()
  } else {
    response.authenticatorData = toBase64url(r.authenticatorData)
    response.signature = toBase64url(r.signature)
    if (r.userHandle) response.userHandle = toBase64url(r.userHandle)
  }
  const out = {
    id: cred.id,
    rawId: toBase64url(cred.rawId),
    type: cred.type,
    response,
    clientExtensionResults: typeof cred.getClientExtensionResults === 'function' ? cred.getClientExtensionResults() : {},
  }
  return cred.authenticatorAttachment ? { ...out, authenticatorAttachment: cred.authenticatorAttachment } : out
}

/**
 * Runs `navigator.credentials.create` or `.get` with the server's options and returns the credential
 * as JSON.
 * @param {'create' | 'get'} kind
 * @param {{ options: any }} answer  What the options call answered.
 * @param {{ signal?: AbortSignal, conditional?: boolean }} [extra]
 * @returns {Promise<Record<string, unknown>>}
 */
export async function ceremony(kind, answer, extra = {}) {
  const creds = /** @type {any} */ (globalThis.navigator)?.credentials
  if (!creds || !globalThis.PublicKeyCredential) throw new PasskeyError('unsupported', 'this browser cannot use passkeys')
  const publicKey = parseOptions(kind, answer.options?.publicKey ?? answer.options)
  /** @type {Record<string, unknown>} */
  const request = { publicKey }
  if (extra.signal) request.signal = extra.signal
  if (extra.conditional) request.mediation = 'conditional'
  let cred
  try {
    cred = await creds[kind](request)
  } catch (err) {
    // An aborted call is the app's own doing: it gets its AbortError back.
    if (/** @type {any} */ (err)?.name === 'AbortError') throw err
    if (/** @type {any} */ (err)?.name === 'NotAllowedError') throw new PasskeyError('cancelled', 'the passkey prompt was dismissed or timed out', err)
    throw new PasskeyError('failed', /** @type {any} */ (err)?.message ?? 'the passkey could not be used', err)
  }
  if (!cred) throw new PasskeyError('cancelled', 'no passkey was chosen')
  return credentialToJSON(cred)
}

/**
 * The signed-in user's passkeys and signing in with one (`client.auth.passkeys`). Adding and removing
 * need a fresh proof: see `Auth#reauth`.
 */
export class Passkeys {
  /** @param {import('./auth.js').Auth} auth */
  constructor(auth) {
    this.auth = auth
  }

  /**
   * What this browser can do: `available` when it can make and use passkeys, `conditional` when it can
   * offer them in the sign-in field's autofill (see {@link Passkeys#signIn}).
   * @returns {Promise<PasskeySupport>}
   */
  async supported() {
    const PKC = /** @type {any} */ (globalThis).PublicKeyCredential
    if (!PKC || !globalThis.navigator?.credentials) return { available: false, conditional: false }
    let conditional = false
    try {
      conditional = typeof PKC.isConditionalMediationAvailable === 'function' ? Boolean(await PKC.isConditionalMediationAvailable()) : false
    } catch {
      // a browser that cannot say cannot do it
    }
    return { available: true, conditional }
  }

  /**
   * Signs in with a passkey, as the first step of a strategy that starts with one. Resolves like
   * `auth.signIn`: the session (stored) or the next step (`'challenge' in result`).
   *
   * With `conditional: true` the call waits quietly until the user picks a passkey from the autofill of
   * an input marked `autocomplete="username webauthn"`; pass a `signal` to stop waiting (when the page
   * offers another way, or the form goes away) and the call rejects with the `AbortError`. Without it
   * the browser shows its passkey prompt at once; a dismissed prompt rejects with a {@link PasskeyError}
   * (`passkey_cancelled`). Every failure of the server is `invalid_passkey`, an `AuthenticationError`.
   * `strategy` is the strategy to sign in with (the client's `strategy` option when left out);
   * `org` signs in to an organization.
   * @param {{ strategy?: string, org?: string, conditional?: boolean, signal?: AbortSignal }} [input]
   * @param {RequestOptions} [opts]
   * @returns {Promise<import('./auth.js').SignInResult>}
   */
  async signIn(input = {}, opts) {
    const { strategy, org, conditional, signal } = input
    const auth = this.auth
    const base = auth.base(org)
    const body = strategy ?? auth.client.strategy ? { strategy: strategy ?? auth.client.strategy } : {}
    const started = (await auth.client.request({ method: 'POST', path: [...base, 'passkey', 'login', 'options'], body, auth: false, ...opts })).data
    const credential = await ceremony('get', started, { signal, conditional })
    /** @type {Record<string, unknown>} */
    const verify = { challenge_id: started.challenge_id, credential }
    if (auth.client.cookies) verify.cookie = true
    const { data } = await auth.client.request({ method: 'POST', path: [...base, 'passkey', 'login', 'verify'], body: verify, auth: false, ...opts })
    return auth.step(data)
  }

  /**
   * The passkeys of the signed-in user, oldest first.
   * @param {RequestOptions} [opts]
   * @returns {Promise<Passkey[]>}
   */
  async list(opts) {
    return (await this.auth.client.request({ method: 'GET', path: ['_auth', 'passkeys'], ...opts })).data.items
  }

  /**
   * Adds a passkey to this device, password manager or security key: the browser asks the user to
   * create it. Needs a fresh proof (`reauth_required` otherwise; see `Auth#reauth` and `Auth#withProof`).
   * Rejects with a {@link PasskeyError} when the prompt is dismissed, `passkey_limit` (a `ConflictError`) at
   * ten, and `invalid_passkey` when the server does not accept what the browser made.
   * @param {{ name?: string }} [input]  What the user calls it (`Phone`; `Passkey` when left out).
   * @param {RequestOptions} [opts]
   * @returns {Promise<Passkey>}
   */
  async register(input = {}, opts) {
    const started = (await this.auth.client.request({ method: 'POST', path: ['_auth', 'passkeys', 'register', 'options'], body: {}, reauth: true, ...opts })).data
    const credential = await ceremony('create', started)
    return (await this.auth.client.request({
      method: 'POST', path: ['_auth', 'passkeys', 'register', 'verify'], reauth: true, body: { challenge_id: started.challenge_id, credential, ...(input.name ? { name: input.name } : {}) }, ...opts,
    })).data
  }

  /**
   * Adds the first passkey during a sign-in that answered with an `EnrollmentChallenge` whose `next` is
   * `passkey` (the strategy asks for one and the user has none). Ends the sign-in: the client holds the
   * session, or the result is the next step.
   * @param {{ challenge: string, name?: string }} input
   * @param {RequestOptions} [opts]
   * @returns {Promise<import('./auth.js').SignInResult>}
   */
  async enroll({ challenge, name }, opts) {
    const started = (await this.auth.client.request({ method: 'POST', path: ['_auth', 'mfa', 'passkey', 'register', 'options'], body: { challenge }, auth: false, ...opts })).data
    const credential = await ceremony('create', started)
    /** @type {Record<string, unknown>} */
    const body = { challenge, credential }
    if (name) body.name = name
    if (this.auth.client.cookies) body.cookie = true
    const { data } = await this.auth.client.request({ method: 'POST', path: ['_auth', 'mfa', 'passkey', 'register', 'verify'], body, auth: false, ...opts })
    return this.auth.step(data)
  }

  /**
   * Renames a passkey.
   * @param {string} id
   * @param {string} name
   * @param {RequestOptions} [opts]
   * @returns {Promise<Passkey>}
   */
  async rename(id, name, opts) {
    return (await this.auth.client.request({ method: 'PATCH', path: ['_auth', 'passkeys', id], body: { name }, ...opts })).data
  }

  /**
   * Removes a passkey. Needs a fresh proof. A passkey the strategy needs and the user holds alone is
   * refused with `credential_required` (a `ConflictError`), and so is the last way to sign in.
   * @param {string} id
   * @param {RequestOptions} [opts]
   * @returns {Promise<void>}
   */
  async remove(id, opts) {
    await this.auth.client.request({ method: 'DELETE', path: ['_auth', 'passkeys', id], reauth: true, ...opts })
  }
}
