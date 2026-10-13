import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, SecondStepRequiredError, AuthenticationError } from '../src/index.js'
import { mockFetch, errorBody, session } from './helpers.js'

const memory = () => {
  /** @type {string | null} */
  let held = null
  return { get: () => held, set: (/** @type {string} */ v) => (held = v), remove: () => (held = null) }
}

/** @param {ReturnType<typeof mockFetch>} m @param {object} [extra] */
const clientOf = (m, extra = {}) => createClient({ url: 'https://api.test', realm: 'acme', fetch: m.fetch, oauthStorage: memory(), ...extra })

const future = () => new Date(Date.now() + 60000).toISOString()

test('signIn returns the second step as a value and stores nothing', async () => {
  const m = mockFetch([
    { body: { mfa_required: true, challenge: 'ch1', methods: ['totp', 'recovery_code'] } },
    { body: { mfa_enrollment_required: true, challenge: 'ch2' } },
  ])
  const c = clientOf(m)
  const a = /** @type {any} */ (await c.auth.signIn({ email: 'a@x.io', password: 'p' }))
  assert.deepEqual(a, { mfaRequired: true, challenge: 'ch1', methods: ['totp', 'recovery_code'] })
  const b = /** @type {any} */ (await c.auth.signIn({ email: 'a@x.io', password: 'p' }))
  assert.deepEqual(b, { enrollmentRequired: true, challenge: 'ch2' })
  assert.equal(await c.auth.token(), null)
})

test('login and signup promise a session: a second step is an error carrying the challenge', async () => {
  const m = mockFetch([
    { body: { mfa_required: true, challenge: 'ch1', methods: ['totp'] } },
    { body: { mfa_enrollment_required: true, challenge: 'ch2' } },
  ])
  const c = clientOf(m)
  await assert.rejects(c.auth.login({ email: 'a@x.io', password: 'p' }), (/** @type {any} */ e) =>
    e instanceof SecondStepRequiredError && e.code === 'mfa_required' && e.challenge === 'ch1' && e.methods[0] === 'totp')
  await assert.rejects(c.auth.signup({ email: 'a@x.io', password: 'p' }), (/** @type {any} */ e) =>
    e instanceof SecondStepRequiredError && e.code === 'mfa_enrollment_required' && e.challenge === 'ch2')
  assert.equal(await c.auth.token(), null)
})

test('verifyMfa trades the challenge and a code for the session', async () => {
  const m = mockFetch([{ body: { ...session('bds_9'), recovery_codes_left: 3 } }])
  const c = clientOf(m)
  const s = await c.auth.verifyMfa('ch1', 'recovery_code', 'AAAAA-BBBBB')
  assert.deepEqual(m.calls[0].body, { challenge: 'ch1', method: 'recovery_code', code: 'AAAAA-BBBBB' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/mfa/verify')
  assert.equal(s.recovery_codes_left, 3)
  assert.equal(await c.auth.token(), 'bds_9')
})

test('mfa management: enrolling from a challenge needs no session and ends in one', async () => {
  const m = mockFetch([
    { body: { setup_id: 's1', secret: 'ABC', otpauth_uri: 'otpauth://x', qr: 'data:image/png;base64,', expires_at: future() } },
    { body: { ...session('bds_5'), recovery_codes: ['AAAAA-BBBBB'] } },
  ])
  const c = clientOf(m)
  const setup = await c.auth.mfa.setup({ challenge: 'ch2' })
  assert.deepEqual(m.calls[0].body, { challenge: 'ch2' })
  assert.equal(m.calls[0].headers.Authorization, undefined)
  const done = await c.auth.mfa.confirm({ challenge: 'ch2', setupId: setup.setup_id, code: '123456' })
  assert.deepEqual(m.calls[1].body, { setup_id: 's1', code: '123456', challenge: 'ch2' })
  assert.deepEqual(done.recovery_codes, ['AAAAA-BBBBB'])
  assert.equal(done.session?.token, 'bds_5')
  assert.equal(await c.auth.token(), 'bds_5')
})

test('mfa management of a signed-in user sends the session and the proof', async () => {
  const m = mockFetch([
    { body: { reauth_token: 'proof', expires_at: future() } },
    { body: { setup_id: 's1', secret: 'ABC', otpauth_uri: 'otpauth://x', qr: 'q', expires_at: future() } },
    { body: { recovery_codes: ['A'] } },
    { body: { recovery_codes: ['B'] } },
    { status: 204 },
    { body: { available: true, enabled: false, methods: [], recovery_codes_left: 0, required: false } },
  ])
  const c = clientOf(m)
  await c.storage.set('bds_1')
  await c.auth.reauth({ method: 'password', password: 'p' })
  await c.auth.mfa.setup()
  assert.equal(m.calls[1].headers['X-Backd-Reauth'], 'proof')
  assert.equal(m.calls[1].headers.Authorization, 'Bearer bds_1')
  assert.deepEqual((await c.auth.mfa.confirm({ setupId: 's1', code: '1' })).recovery_codes, ['A'])
  assert.deepEqual(await c.auth.mfa.regenerateRecoveryCodes(), ['B'])
  await c.auth.mfa.disable()
  assert.equal(m.calls[4].method, 'DELETE')
  assert.equal(m.calls[4].url.pathname, '/v1/acme/_auth/mfa/totp')
  assert.equal((await c.auth.mfa.status()).enabled, false)
})

test('withProof asks for the proof when the server wants one, and tries once more', async () => {
  const m = mockFetch([
    { status: 401, body: errorBody('reauth_required') },
    { body: { id: 'u', email: 'a@x.io', email_verified: true, roles: [], locale: 'en', created_at: 'x', identities: [], reauth_methods: ['totp', 'recovery_code'], sign_in_methods: [] } },
    { body: { reauth_token: 'proof', expires_at: future() } },
    { status: 204 },
  ])
  const c = clientOf(m)
  await c.storage.set('bds_1')
  /** @type {string[]} */
  let offered = []
  await c.auth.withProof(async (methods) => { offered = methods; return { method: 'totp', code: '123456' } }, () => c.auth.mfa.disable())
  assert.deepEqual(offered, ['totp', 'recovery_code'])
  assert.deepEqual(m.calls[2].body, { method: 'totp', code: '123456' })
  assert.equal(m.calls[3].headers['X-Backd-Reauth'], 'proof')
  // Another error is not a reason to ask.
  const m2 = mockFetch([{ status: 401, body: errorBody('invalid_credentials') }])
  const c2 = clientOf(m2)
  await c2.storage.set('bds_1')
  await assert.rejects(c2.auth.withProof(async () => ({ method: 'password', password: 'x' }), () => c2.auth.mfa.disable()), AuthenticationError)
})

test('a magic link: the request keeps the verifier, the code finishes the sign-in', async () => {
  const m = mockFetch([
    { status: 202, body: { request_id: 'req-1' } },
    { body: { ...session('bds_7'), new_user: true } },
    { status: 202, body: { request_id: 'req-2' } },
    { body: { mfa_required: true, challenge: 'ch', methods: ['totp'] } },
  ])
  const c = clientOf(m)
  const asked = await c.auth.magicLink.request('Ada@Example.com', { redirectTo: 'https://app.test/in' })
  assert.equal(asked.requestId, 'req-1')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/magic-link')
  assert.equal(m.calls[0].body.redirect_to, 'https://app.test/in')
  assert.match(m.calls[0].body.code_challenge, /^[A-Za-z0-9_-]{43}$/)
  const s = /** @type {any} */ (await c.auth.magicLink.verifyCode(' 123456 '))
  assert.equal(m.calls[1].body.request_id, 'req-1')
  assert.equal(m.calls[1].body.code, '123456')
  assert.equal(m.calls[1].body.code_verifier.length, 43)
  assert.equal(s.new_user, true)
  assert.equal(await c.auth.token(), 'bds_7')
  // The request is spent; and with a second factor the code ends in the step.
  await assert.rejects(c.auth.magicLink.verifyCode('1'), (/** @type {any} */ e) => e.code === 'no_request')
  await c.auth.magicLink.request('ada@example.com', { redirectTo: 'https://app.test/in' })
  const step = /** @type {any} */ (await c.auth.magicLink.verifyCode('654321'))
  assert.equal(step.mfaRequired, true)
})

test('password.set and password.remove', async () => {
  const m = mockFetch([{ status: 204 }, { status: 204 }])
  const c = clientOf(m)
  await c.storage.set('bds_1')
  await c.auth.password.set({ newPassword: 'a-new-password-1' })
  assert.deepEqual(m.calls[0].body, { new_password: 'a-new-password-1' })
  await c.auth.password.remove()
  assert.equal(m.calls[1].method, 'DELETE')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/password')
})

test('the return of a link for a user with a second factor is a step too', async () => {
  const store = memory()
  store.set(JSON.stringify({ verifier: 'v'.repeat(43), provider: 'magic-link', intent: 'signin', at: Date.now() }))
  const m = mockFetch([{ body: { mfa_required: true, challenge: 'chX', methods: ['totp'] } }])
  const c = clientOf(m, { oauthStorage: store })
  const out = /** @type {any} */ (await c.auth.completeSignIn('https://app.test/in?backd_code=c'))
  assert.equal(out.mfaRequired, true)
  assert.equal(out.challenge, 'chX')
  assert.equal(await c.auth.token(), null)
})
