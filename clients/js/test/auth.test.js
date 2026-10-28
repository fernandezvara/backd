import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, AuthenticationError, PasswordExpiredError, NetworkError, RetryableError, ValidationError, VerificationRequiredError, localStorageStorage, memoryStorage } from '../src/index.js'
import { mockFetch, errorBody, session, user } from './helpers.js'

/** @param {ReturnType<typeof mockFetch>} m */
function clientWith(m, opts = {}) {
  const c = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, ...opts })
  /** @type {Array<[string, any]>} */
  const events = []
  c.auth.onAuthChange((e, s) => events.push([e, s]))
  return { c, events }
}

test('signup stores the session and announces it', async () => {
  const m = mockFetch([{ status: 201, body: session('bds_1') }, { status: 201, body: session('bds_2') }])
  const { c, events } = clientWith(m)
  const s = await c.auth.signup({ email: 'ada@example.com', password: 'dev-p4ssw0rd!' })
  assert.equal(s.token, 'bds_1')
  assert.equal(await c.auth.token(), 'bds_1')
  assert.equal(m.calls[0].method, 'POST')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/signup')
  assert.deepEqual(m.calls[0].body, { email: 'ada@example.com', password: 'dev-p4ssw0rd!' })
  assert.equal(m.calls[0].headers.Authorization, undefined, 'no credentials on signup')
  assert.deepEqual(events, [['SIGNED_IN', s]])

  await c.auth.signup({ email: 'b@example.com', password: 'dev-p4ssw0rd!', invitation: 'bdi_x' })
  assert.equal(m.calls[1].body.invitation, 'bdi_x')
})

test('signup in a realm that requires verified addresses starts no session', async () => {
  const m = mockFetch([{ status: 202, body: { status: 'verification_required' } }])
  const { c, events } = clientWith(m)
  await assert.rejects(
    c.auth.signup({ email: 'ada@example.com', password: 'dev-p4ssw0rd!', redirectTo: 'https://app.example/welcome' }),
    (/** @type {any} */ e) => e instanceof VerificationRequiredError && e.code === 'verification_required',
  )
  assert.equal(await c.auth.token(), null)
  assert.deepEqual(events, [])
  assert.deepEqual(m.calls[0].body, { email: 'ada@example.com', password: 'dev-p4ssw0rd!', redirect_to: 'https://app.example/welcome' })
})

test('login with an unverified address is a forbidden error with its code', async () => {
  const m = mockFetch([{ status: 403, body: { error: { code: 'email_not_verified', message: 'verify your email address before signing in', request_id: 'r1' } } }])
  const { c } = clientWith(m)
  await assert.rejects(c.auth.login({ email: 'ada@example.com', password: 'dev-p4ssw0rd!' }), (/** @type {any} */ e) => e.code === 'email_not_verified' && e.status === 403)
})

test('login stores the token; later requests send it', async () => {
  const m = mockFetch([{ body: session('bds_1') }, { body: user }])
  const { c, events } = clientWith(m)
  await c.auth.login({ email: 'ada@example.com', password: 'pw' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/login')
  assert.deepEqual(await c.auth.me(), user)
  assert.equal(m.calls[1].headers.Authorization, 'Bearer bds_1')
  assert.equal(events[0][0], 'SIGNED_IN')
})

test('a failed login throws without touching the stored session', async () => {
  const m = mockFetch([{ status: 401, body: errorBody('invalid_credentials') }])
  const { c, events } = clientWith(m)
  await c.storage.set('bds_old')
  await assert.rejects(c.auth.login({ email: 'a@x.io', password: 'bad' }), (/** @type {any} */ e) => e instanceof AuthenticationError && e.code === 'invalid_credentials')
  assert.equal(await c.auth.token(), 'bds_old')
  assert.deepEqual(events, [])
})

test('an expired password is a typed error that says whether the reset email was queued', async () => {
  const body = (/** @type {Array<{ path: string, reason: string }>} */ details) => ({ error: { code: 'password_expired', message: 'this password has expired', details, request_id: 'r1' } })
  const m = mockFetch([
    { status: 401, body: body([{ path: 'reset_email', reason: 'queued' }]) },
    { status: 401, body: body([]) },
    { status: 401, body: body([{ path: 'reset_email', reason: 'queued' }]) },
    { status: 202, body: { status: 'accepted' } },
  ])
  const { c, events } = clientWith(m)
  await assert.rejects(c.auth.login({ email: 'a@x.io', password: 'dev-p4ssw0rd!' }), (/** @type {any} */ e) =>
    e instanceof PasswordExpiredError && e instanceof AuthenticationError && e.status === 401 && e.code === 'password_expired' && e.resetEmailQueued === true)
  await assert.rejects(c.auth.signIn({ email: 'a@x.io', password: 'dev-p4ssw0rd!' }), (/** @type {any} */ e) => e instanceof PasswordExpiredError && e.resetEmailQueued === false)
  assert.deepEqual(events, [], 'a refused sign-in announces nothing')

  // Proving yourself again with an expired password is refused the same way, and does not end the session.
  await c.storage.set('bds_live')
  await assert.rejects(c.auth.reauth({ method: 'password', password: 'dev-p4ssw0rd!' }), PasswordExpiredError)
  assert.equal(await c.auth.token(), 'bds_live')
  assert.deepEqual(events, [])

  // The same flow as a user's Forgot password starts the email when none was queued.
  await c.auth.requestPasswordReset({ email: 'a@x.io' })
  assert.equal(m.calls[3].url.pathname, '/v1/acme/_auth/reset-password/request')
})

test('a refused session token expires the session', async () => {
  const m = mockFetch([{ status: 401, body: errorBody('unauthenticated'), headers: { 'WWW-Authenticate': 'Bearer realm="acme", error="invalid_token"' } }])
  const { c, events } = clientWith(m)
  await c.storage.set('bds_old')
  await assert.rejects(c.auth.me(), AuthenticationError)
  assert.equal(await c.auth.token(), null)
  assert.deepEqual(events, [['SESSION_EXPIRED', null]])
})

test('a session that belongs to no organization expires like a refused one', async () => {
  // A session from before sessions were scoped: the realm has organizations now.
  const m = mockFetch([{ status: 401, body: errorBody('session_unscoped') }])
  const { c, events } = clientWith(m)
  await c.storage.set('bds_old')
  await assert.rejects(c.auth.me(), AuthenticationError)
  assert.equal(await c.auth.token(), null)
  assert.deepEqual(events, [['SESSION_EXPIRED', null]])
})

test('logout ends the session, even one the server already forgot', async () => {
  const m = mockFetch([{ status: 204 }, { status: 401, body: errorBody('unauthenticated') }])
  const { c, events } = clientWith(m)
  await c.storage.set('bds_1')
  await c.auth.logout()
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/logout')
  assert.equal(m.calls[0].headers.Authorization, 'Bearer bds_1')
  assert.equal(await c.auth.token(), null)

  await c.storage.set('bds_2')
  await c.auth.logout() // 401: already gone, not an error
  assert.equal(await c.auth.token(), null)
  assert.deepEqual(events, [['SIGNED_OUT', null], ['SIGNED_OUT', null]])
})

test('logout removes the token even when the server is unreachable', async () => {
  const m = mockFetch([new TypeError('fetch failed')])
  const { c, events } = clientWith(m)
  await c.storage.set('bds_1')
  await assert.rejects(c.auth.logout(), NetworkError)
  assert.equal(await c.auth.token(), null)
  assert.deepEqual(events, [['SIGNED_OUT', null]])
})

test('logoutAll, deleteAccount, changePassword, sessions, revokeSession', async () => {
  const sessions = [{ id: 's1', created_at: 'x', last_used_at: 'x', expires_at: 'x', current: true }]
  const m = mockFetch([
    { body: { reauth_token: 'proof-1', expires_at: new Date(Date.now() + 5 * 60 * 1000).toISOString() } },
    { status: 204 }, { status: 204 }, { status: 204 }, { body: { items: sessions } }, { status: 204 },
  ])
  const { c, events } = clientWith(m)
  await c.storage.set('bds_1')
  // A sensitive action carries the fresh proof the client holds; without one it doesn't.
  assert.equal(c.auth.hasFreshProof(), false)
  const proof = await c.auth.reauth({ method: 'password', password: 'old' })
  assert.deepEqual(m.calls[0].body, { method: 'password', password: 'old' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/reauth')
  assert.equal(proof.reauthToken, 'proof-1')
  assert.equal(c.auth.hasFreshProof(), true)
  await c.auth.changePassword({ newPassword: 'new' })
  assert.deepEqual(m.calls[1].body, { new_password: 'new' })
  assert.equal(m.calls[1].headers['X-Backd-Reauth'], 'proof-1')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/password')
  assert.equal(await c.auth.token(), 'bds_1', 'this session stays valid')

  await c.auth.logoutAll()
  assert.equal(m.calls[2].url.pathname, '/v1/acme/_auth/logout-all')
  assert.equal(await c.auth.token(), null)
  assert.equal(c.auth.hasFreshProof(), false, 'the proof ends with the session')

  await c.storage.set('bds_2')
  await c.auth.deleteAccount()
  assert.equal(m.calls[3].method, 'DELETE')
  assert.equal(m.calls[3].url.pathname, '/v1/acme/_auth/me')
  assert.equal(m.calls[3].body, undefined)
  assert.equal(m.calls[3].headers['X-Backd-Reauth'], undefined)
  assert.equal(await c.auth.token(), null)

  await c.storage.set('bds_3')
  assert.deepEqual(await c.auth.sessions(), sessions)
  await c.auth.revokeSession('s/1')
  assert.equal(m.calls[5].url.pathname, '/v1/acme/_auth/sessions/s%2F1')
  assert.deepEqual(events.map((e) => e[0]), ['SIGNED_OUT', 'SIGNED_OUT'])
})

test('listeners can unsubscribe, and a failing one breaks nothing', async () => {
  const m = mockFetch([{ body: session('bds_1') }, { body: session('bds_2') }])
  const c = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch })
  /** @type {string[]} */
  const seen = []
  const stop = c.auth.onAuthChange((e) => seen.push(e))
  c.auth.onAuthChange(() => {
    throw new Error('listener bug')
  })
  /** @type {unknown[][]} */
  const logged = []
  const orig = console.error
  console.error = (...args) => logged.push(args)
  try {
    await c.auth.login({ email: 'a@x.io', password: 'pw' })
  } finally {
    console.error = orig
  }
  assert.equal(logged.length, 1)
  assert.equal(/** @type {Error} */ (logged[0][1]).message, 'listener bug')
  assert.equal(await c.auth.token(), 'bds_1', 'the client kept working')
  stop()
  console.error = () => {}
  try {
    await c.auth.login({ email: 'a@x.io', password: 'pw' })
  } finally {
    console.error = orig
  }
  assert.deepEqual(seen, ['SIGNED_IN'])
})

test('custom and async storage', async () => {
  /** @type {string | null} */
  let saved = null
  const storage = {
    get: async () => saved,
    set: async (/** @type {string} */ t) => {
      saved = t
    },
    remove: async () => {
      saved = null
    },
  }
  const m = mockFetch([{ body: session('bds_async') }, { body: user }])
  const c = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, storage })
  await c.auth.login({ email: 'a@x.io', password: 'pw' })
  assert.equal(saved, 'bds_async')
  await c.auth.me()
  assert.equal(m.calls[1].headers.Authorization, 'Bearer bds_async')
})

test('localStorage adapter', () => {
  const g = /** @type {any} */ (globalThis)
  const had = 'localStorage' in g
  const saved = g.localStorage
  /** @type {Map<string, string>} */
  const data = new Map()
  g.localStorage = { getItem: (/** @type {string} */ k) => data.get(k) ?? null, setItem: (/** @type {string} */ k, /** @type {string} */ v) => data.set(k, v), removeItem: (/** @type {string} */ k) => data.delete(k) }
  try {
    const s = localStorageStorage('k')
    s.set('bds_1')
    assert.equal(data.get('k'), 'bds_1')
    assert.equal(s.get(), 'bds_1')
    s.remove()
    assert.equal(s.get(), null)
  } finally {
    if (had) g.localStorage = saved
    else delete g.localStorage
  }
  assert.throws(() => localStorageStorage(), /not available/)
  assert.equal(memoryStorage().get(), null)
})

test('signup can name the user\'s language', async () => {
  const m = mockFetch([{ status: 201, body: session('bds_1') }])
  const { c } = clientWith(m)
  await c.auth.signup({ email: 'ada@example.com', password: 'dev-p4ssw0rd!', locale: 'es-MX' })
  assert.deepEqual(m.calls[0].body, { email: 'ada@example.com', password: 'dev-p4ssw0rd!', locale: 'es-MX' })
})

test('updateMe changes the language, and an unlisted one is a validation error', async () => {
  const m = mockFetch([
    { status: 200, body: { ...user, locale: 'es' } },
    { status: 400, body: { error: { code: 'invalid_locale', message: "this realm doesn't offer that language", request_id: 'r1', details: [{ path: 'locale', reason: 'locale must be one of: en, es' }] } } },
  ])
  const { c } = clientWith(m)
  const me = await c.auth.updateMe({ locale: 'es' })
  assert.equal(me.locale, 'es')
  assert.equal(m.calls[0].method, 'PATCH')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/me')
  assert.deepEqual(m.calls[0].body, { locale: 'es' })
  await assert.rejects(() => c.auth.updateMe({ locale: 'fr' }), (/** @type {any} */ e) => e.code === 'invalid_locale' && e.status === 400 && e.details[0].path === 'locale')
})

test('me carries the sign-in methods', async () => {
  const identities = [{ provider: 'password', email: 'ada@example.com', email_verified: true, created_at: 'x', last_used_at: null }]
  const m = mockFetch([{ status: 200, body: { ...user, identities } }])
  const { c } = clientWith(m)
  const me = await c.auth.me()
  assert.equal(me.identities[0].provider, 'password')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/me')
})

test('the email flows send their bodies without a session and answer nothing', async () => {
  const m = mockFetch([{ status: 202, body: { status: 'accepted' } }, { status: 204 }, { status: 202, body: { status: 'accepted' } }, { status: 204 }, { status: 204 }, { status: 204 }, { status: 204 }, { status: 204 }])
  const { c, events } = clientWith(m)
  assert.equal(await c.auth.resendVerification({ email: 'ada@example.com' }), undefined)
  assert.equal(await c.auth.verifyEmail('tok1'), undefined)
  await c.auth.requestPasswordReset({ email: 'ada@example.com', redirectTo: 'https://app.example/signin' })
  await c.auth.resetPassword({ token: 'tok2', password: 'dev-p4ssw0rd!2' })
  await c.auth.updatePassword({ email: 'ada@example.com', password: 'dev-p4ssw0rd!', newPassword: 'dev-p4ssw0rd!2', code: '123456' })
  await c.auth.confirmEmailChange('tok3')
  await c.auth.revertEmailChange('tok4')
  await c.auth.acceptInvitation({ token: 'tok5', password: 'dev-p4ssw0rd!', locale: 'es' })
  const sent = m.calls.map((call) => [call.method, call.url.pathname, call.body])
  assert.deepEqual(sent, [
    ['POST', '/v1/acme/_auth/verify-email/resend', { email: 'ada@example.com' }],
    ['POST', '/v1/acme/_auth/verify-email', { token: 'tok1' }],
    ['POST', '/v1/acme/_auth/reset-password/request', { email: 'ada@example.com', redirect_to: 'https://app.example/signin' }],
    ['POST', '/v1/acme/_auth/reset-password', { token: 'tok2', password: 'dev-p4ssw0rd!2' }],
    ['POST', '/v1/acme/_auth/password/update', { email: 'ada@example.com', password: 'dev-p4ssw0rd!', new_password: 'dev-p4ssw0rd!2', code: '123456' }],
    ['POST', '/v1/acme/_auth/confirm-email-change', { token: 'tok3' }],
    ['POST', '/v1/acme/_auth/revert-email-change', { token: 'tok4' }],
    ['POST', '/v1/acme/_auth/accept-invitation', { token: 'tok5', password: 'dev-p4ssw0rd!', locale: 'es' }],
  ])
  assert.ok(m.calls.every((call) => call.headers.Authorization === undefined), 'these calls carry no credentials')
  assert.equal(await c.auth.token(), null, 'none of them starts a session')
  assert.deepEqual(events, [])
})

test('a bad token is a validation error with the invalid_token code, and limits are retryable', async () => {
  const m = mockFetch([
    { status: 400, body: errorBody('invalid_token', 'invalid, expired or used token') },
    { status: 400, body: { error: { code: 'validation_error', message: 'password does not meet the policy', request_id: 'r1', details: [{ path: 'password', reason: 'must be at least 12 characters' }] } } },
    { status: 429, headers: { 'Retry-After': '30' }, body: errorBody('too_many_requests') },
  ])
  const { c } = clientWith(m)
  await assert.rejects(c.auth.verifyEmail('nope'), (/** @type {any} */ e) => e instanceof ValidationError && e.code === 'invalid_token')
  await assert.rejects(c.auth.resetPassword({ token: 't', password: 'short' }), (/** @type {any} */ e) => e.details[0].path === 'password')
  await assert.rejects(c.auth.requestPasswordReset({ email: 'ada@example.com' }), (/** @type {any} */ e) => e instanceof RetryableError && e.retryAfter === 30000)
})

test('requesting an email change sends the session and the fresh proof', async () => {
  const m = mockFetch([
    { body: session('bds_1') },
    { body: { reauth_token: 'proof-2', expires_at: new Date(Date.now() + 60000).toISOString() } },
    { status: 202, body: { status: 'accepted' } },
  ])
  const { c } = clientWith(m)
  await c.auth.login({ email: 'ada@example.com', password: 'dev-p4ssw0rd!' })
  await c.auth.reauth({ method: 'password', password: 'dev-p4ssw0rd!' })
  await c.auth.requestEmailChange({ newEmail: 'ada.new@example.com', redirectTo: 'https://app.example/account' })
  assert.equal(m.calls[2].url.pathname, '/v1/acme/_auth/email')
  assert.equal(m.calls[2].headers.Authorization, 'Bearer bds_1')
  assert.equal(m.calls[2].headers['X-Backd-Reauth'], 'proof-2')
  assert.deepEqual(m.calls[2].body, { new_email: 'ada.new@example.com', redirect_to: 'https://app.example/account' })
})

test('an expired proof is not sent, and a code can be asked for by email', async () => {
  const m = mockFetch([
    { body: { reauth_token: 'old', expires_at: new Date(Date.now() - 1000).toISOString() } },
    { status: 204 },
    { status: 202, body: { status: 'accepted' } },
    { body: { reauth_token: 'p3', expires_at: new Date(Date.now() + 60000).toISOString() } },
  ])
  const { c } = clientWith(m)
  await c.storage.set('bds_1')
  await c.auth.reauth({ method: 'password', password: 'x' })
  assert.equal(c.auth.hasFreshProof(), false)
  await c.auth.changePassword({ newPassword: 'new-password-1' })
  assert.equal(m.calls[1].headers['X-Backd-Reauth'], undefined)
  await c.auth.requestReauthCode()
  assert.equal(m.calls[2].url.pathname, '/v1/acme/_auth/reauth/email-code')
  await c.auth.reauth({ method: 'email_code', code: '123456' })
  assert.deepEqual(m.calls[3].body, { method: 'email_code', code: '123456' })
})

test('cookies: true keeps the session in a cookie, out of the client', async () => {
  const sessionNoToken = { session_id: 's1', expires_at: '2026-10-01T00:00:00.000Z', user }
  const m = mockFetch([
    { body: sessionNoToken },                       // login
    { body: user },                                 // me
    { status: 401, body: errorBody('unauthenticated') }, // a later request the server refuses
    { body: sessionNoToken },                       // login again
    { status: 204 },                                // logout
  ])
  const { c, events } = clientWith(m, { cookies: true })
  const s = await c.auth.login({ email: 'ada@example.com', password: 'dev-p4ssw0rd!' })
  assert.deepEqual(m.calls[0].body, { email: 'ada@example.com', password: 'dev-p4ssw0rd!', cookie: true })
  assert.equal(m.calls[0].credentials, 'include')
  assert.equal(s.token, undefined)
  assert.equal(await c.auth.token(), null, 'there is no token for scripts to read')
  assert.equal(await c.auth.hasSession(), true)

  await c.auth.me()
  assert.equal(m.calls[1].headers.Authorization, undefined, 'the browser sends the cookie, not the client')
  assert.equal(m.calls[1].credentials, 'include')

  // A refusal while a session was believed to exist ends it.
  await assert.rejects(c.auth.me(), AuthenticationError)
  assert.equal(await c.auth.hasSession(), false)
  assert.deepEqual(events.map((e) => e[0]), ['SIGNED_IN', 'SESSION_EXPIRED'])

  await c.auth.login({ email: 'ada@example.com', password: 'dev-p4ssw0rd!' })
  await c.auth.logout()
  assert.equal(await c.auth.hasSession(), false)
  assert.equal(m.calls[4].credentials, 'include')
})

test('cookies: sign-up asks for a cookie too; an API key and cookies do not mix; without the option nothing changes', async () => {
  const m = mockFetch([{ status: 201, body: { session_id: 's1', expires_at: '2026-10-01T00:00:00.000Z', user } }, { body: session('bds_1') }])
  const { c } = clientWith(m, { cookies: true })
  await c.auth.signup({ email: 'ada@example.com', password: 'dev-p4ssw0rd!' })
  assert.equal(m.calls[0].body.cookie, true)

  assert.throws(() => createClient({ url: 'http://api.test', realm: 'acme', apiKey: 'bdk_x', cookies: true, fetch: m.fetch }), /cookies/)

  const plain = clientWith(m).c
  await plain.auth.login({ email: 'ada@example.com', password: 'dev-p4ssw0rd!' })
  assert.equal(m.calls[1].body.cookie, undefined)
  assert.equal(m.calls[1].credentials, undefined)
  assert.equal(await plain.auth.hasSession(), true)
})
