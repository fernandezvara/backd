import { test, afterEach } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, PasskeyError } from '../src/index.js'
import { fromBase64url, toBase64url } from '../src/passkeys.js'
import { mockFetch, session } from './helpers.js'

const g = /** @type {any} */ (globalThis)
const saved = { PublicKeyCredential: g.PublicKeyCredential, navigator: Object.getOwnPropertyDescriptor(g, 'navigator') }

/**
 * A browser with passkeys: `navigator.credentials` runs `handler(kind, request)` and answers a credential
 * that has no `toJSON`, so the client's own conversion is the one under test.
 * @param {(kind: string, request: any) => any} handler
 * @param {object} [native]  Platform statics (`parseCreationOptionsFromJSON`…) to add to `PublicKeyCredential`.
 */
function browser(handler, native = {}) {
  g.PublicKeyCredential = Object.assign(function PublicKeyCredential() {}, { isConditionalMediationAvailable: async () => true }, native)
  Object.defineProperty(g, 'navigator', {
    configurable: true,
    value: { credentials: { create: async (/** @type {any} */ r) => handler('create', r), get: async (/** @type {any} */ r) => handler('get', r) } },
  })
}

afterEach(() => {
  if (saved.PublicKeyCredential === undefined) delete g.PublicKeyCredential
  else g.PublicKeyCredential = saved.PublicKeyCredential
  if (saved.navigator) Object.defineProperty(g, 'navigator', saved.navigator)
  else delete g.navigator
})

const bytes = (/** @type {number[]} */ ...n) => new Uint8Array(n).buffer

const assertion = {
  id: 'AQID', rawId: bytes(1, 2, 3), type: 'public-key', authenticatorAttachment: 'platform',
  response: { clientDataJSON: bytes(4, 5), authenticatorData: bytes(6), signature: bytes(7, 8), userHandle: bytes(9) },
  getClientExtensionResults: () => ({}),
}
const attestation = {
  id: 'AQID', rawId: bytes(1, 2, 3), type: 'public-key',
  response: { clientDataJSON: bytes(4, 5), attestationObject: bytes(10), getTransports: () => ['internal'], getPublicKeyAlgorithm: () => -7 },
  getClientExtensionResults: () => ({}),
}
const getOptions = { publicKey: { challenge: 'AAEC', rpId: 'localhost', userVerification: 'required', allowCredentials: [{ type: 'public-key', id: 'AQID' }] } }
const createOptions = { publicKey: { challenge: 'AAEC', rp: { id: 'localhost', name: 'x' }, user: { id: 'dQ', name: 'a', displayName: 'a' }, excludeCredentials: [{ type: 'public-key', id: 'AQID' }] } }

function clientWith(/** @type {ReturnType<typeof mockFetch>} */ m, opts = {}) {
  return createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, ...opts })
}

test('base64url round trip', () => {
  for (const n of [[], [1], [1, 2], [1, 2, 3], [250, 251, 252, 253]]) {
    const b = new Uint8Array(n)
    const s = toBase64url(b)
    assert.ok(!/[+/=]/.test(s))
    assert.deepEqual([...new Uint8Array(fromBase64url(s))], n)
  }
  assert.equal(toBase64url(bytes(251, 255)), '-_8')
})

test('supported reports what the browser has', async () => {
  const c = clientWith(mockFetch([]))
  delete g.PublicKeyCredential
  assert.deepEqual(await c.auth.passkeys.supported(), { available: false, conditional: false })
  browser(() => null)
  assert.deepEqual(await c.auth.passkeys.supported(), { available: true, conditional: true })
  g.PublicKeyCredential.isConditionalMediationAvailable = undefined
  assert.deepEqual(await c.auth.passkeys.supported(), { available: true, conditional: false })
})

test('signIn runs both calls and stores the session', async () => {
  const m = mockFetch([{ body: { challenge_id: 'c1', strategy: 'users_passkey', options: getOptions } }, { body: session('bds_pk') }])
  const c = clientWith(m)
  /** @type {any} */
  let seen
  browser((kind, req) => { seen = { kind, req }; return assertion })
  const out = await c.auth.passkeys.signIn({ strategy: 'users_passkey' })
  assert.equal(/** @type {any} */ (out).token, 'bds_pk')
  assert.equal(await c.auth.token(), 'bds_pk')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/passkey/login/options')
  assert.deepEqual(m.calls[0].body, { strategy: 'users_passkey' })
  assert.equal(seen.kind, 'get')
  assert.ok(seen.req.publicKey.challenge instanceof ArrayBuffer)
  assert.deepEqual([...new Uint8Array(seen.req.publicKey.allowCredentials[0].id)], [1, 2, 3])
  assert.equal(seen.req.mediation, undefined)
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/passkey/login/verify')
  assert.equal(m.calls[1].body.challenge_id, 'c1')
  assert.deepEqual(m.calls[1].body.credential, {
    id: 'AQID', rawId: 'AQID', type: 'public-key', authenticatorAttachment: 'platform', clientExtensionResults: {},
    response: { clientDataJSON: 'BAU', authenticatorData: 'Bg', signature: 'Bwg', userHandle: 'CQ' },
  })
  assert.equal(m.calls[1].headers.Authorization, undefined)
})

test('signIn uses the platform parsing and toJSON when there are some', async () => {
  const m = mockFetch([{ body: { challenge_id: 'c1', strategy: 's', options: getOptions } }, { body: session('bds_pk') }])
  const c = clientWith(m)
  const parsed = { parsed: true }
  /** @type {any} */
  let seen
  browser((_k, req) => { seen = req; return { toJSON: () => ({ id: 'native' }) } }, { parseRequestOptionsFromJSON: () => parsed })
  await c.auth.passkeys.signIn()
  assert.equal(seen.publicKey, parsed)
  assert.deepEqual(m.calls[1].body.credential, { id: 'native' })
  assert.deepEqual(m.calls[0].body, {})
})

test('signIn conditional passes mediation and the signal; an abort stays an abort', async () => {
  const m = mockFetch([{ body: { challenge_id: 'c1', strategy: 's', options: getOptions } }])
  const c = clientWith(m, { strategy: 's' })
  const ac = new AbortController()
  /** @type {any} */
  let seen
  browser((_k, req) => {
    seen = req
    throw Object.assign(new Error('aborted'), { name: 'AbortError' })
  })
  await assert.rejects(c.auth.passkeys.signIn({ conditional: true, signal: ac.signal }), (/** @type {any} */ e) => e.name === 'AbortError')
  assert.equal(seen.mediation, 'conditional')
  assert.equal(seen.signal, ac.signal)
  assert.deepEqual(m.calls[0].body, { strategy: 's' })
  assert.equal(m.remaining(), 0)
})

test('a dismissed prompt is a PasskeyError and nothing is verified', async () => {
  const m = mockFetch([{ body: { challenge_id: 'c1', strategy: 's', options: getOptions } }])
  const c = clientWith(m)
  browser(() => { throw Object.assign(new Error('no'), { name: 'NotAllowedError' }) })
  await assert.rejects(c.auth.passkeys.signIn(), (/** @type {any} */ e) => e instanceof PasskeyError && e.code === 'passkey_cancelled')
  delete g.PublicKeyCredential
  const m2 = mockFetch([{ body: { challenge_id: 'c1', strategy: 's', options: getOptions } }])
  await assert.rejects(clientWith(m2).auth.passkeys.signIn(), (/** @type {any} */ e) => e.code === 'passkey_unsupported')
})

test('signIn in an organization uses its routes and returns a next step', async () => {
  const m = mockFetch([
    { body: { challenge_id: 'c1', strategy: 's', options: getOptions } },
    { body: { mfa_required: true, challenge: 'ch', methods: ['totp'], next: ['totp'], strategy: 's' } },
  ])
  const c = clientWith(m)
  browser(() => assertion)
  const out = await c.auth.passkeys.signIn({ org: 'acme-corp' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/orgs/acme-corp/passkey/login/options')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/orgs/acme-corp/passkey/login/verify')
  assert.deepEqual(out, { mfaRequired: true, challenge: 'ch', methods: ['totp'], next: ['totp'], strategy: 's' })
  assert.equal(await c.auth.token(), null)
})

test('the later step of a strategy runs the ceremony', async () => {
  const m = mockFetch([{ body: { options: getOptions } }, { body: session('bds_2') }])
  const c = clientWith(m)
  browser(() => assertion)
  const out = await c.auth.verifyStep('ch', 'passkey')
  assert.equal(/** @type {any} */ (out).token, 'bds_2')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/mfa/passkey/options')
  assert.deepEqual(m.calls[0].body, { challenge: 'ch' })
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/mfa/verify')
  assert.equal(m.calls[1].body.method, 'passkey')
  assert.equal(m.calls[1].body.code, undefined)
  assert.equal(m.calls[1].body.credential.rawId, 'AQID')
})

test('a code step is unchanged', async () => {
  const m = mockFetch([{ body: session('bds_3') }])
  await clientWith(m).auth.verifyStep('ch', 'totp', '123456')
  assert.deepEqual(m.calls[0].body, { challenge: 'ch', method: 'totp', code: '123456' })
})

test('reauth with a passkey', async () => {
  const m = mockFetch([{ body: session('bds_1') }, { body: { options: getOptions } }, { body: { reauth_token: 'r1', expires_at: new Date(Date.now() + 60000).toISOString() } }])
  const c = clientWith(m)
  await c.auth.signup({ email: 'a@example.com', password: 'dev-p4ssw0rd!' })
  browser(() => assertion)
  const r = await c.auth.reauth({ method: 'passkey' })
  assert.equal(r.reauthToken, 'r1')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/reauth/passkey/options')
  assert.equal(m.calls[2].url.pathname, '/v1/acme/_auth/reauth')
  assert.equal(m.calls[2].body.method, 'passkey')
  assert.equal(m.calls[2].body.credential.id, 'AQID')
  assert.ok(c.auth.hasFreshProof())
})

test('register asks for the options, makes the passkey and sends it with the proof', async () => {
  const stored = { id: 'p1', name: 'Laptop', created_at: 'x', last_used_at: null, transports: ['internal'], backup_eligible: true, backup_state: false }
  const m = mockFetch([{ body: { challenge_id: 'c9', options: createOptions } }, { status: 201, body: stored }])
  const c = clientWith(m)
  c.proof = { token: 'r1', expires: Date.now() + 60000 }
  /** @type {any} */
  let seen
  browser((kind, req) => { seen = { kind, req }; return attestation })
  assert.deepEqual(await c.auth.passkeys.register({ name: 'Laptop' }), stored)
  assert.equal(seen.kind, 'create')
  assert.deepEqual([...new Uint8Array(seen.req.publicKey.user.id)], [117])
  assert.deepEqual([...new Uint8Array(seen.req.publicKey.excludeCredentials[0].id)], [1, 2, 3])
  assert.equal(m.calls[0].headers['X-Backd-Reauth'], 'r1')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/passkeys/register/verify')
  assert.equal(m.calls[1].headers['X-Backd-Reauth'], 'r1')
  assert.equal(m.calls[1].body.challenge_id, 'c9')
  assert.equal(m.calls[1].body.name, 'Laptop')
  assert.deepEqual(m.calls[1].body.credential.response, { clientDataJSON: 'BAU', attestationObject: 'Cg', transports: ['internal'], publicKeyAlgorithm: -7 })
})

test('enroll finishes a sign-in with the first passkey', async () => {
  const m = mockFetch([{ body: { challenge_id: 'c9', options: createOptions } }, { body: session('bds_4') }])
  const c = clientWith(m)
  browser(() => attestation)
  const out = await c.auth.passkeys.enroll({ challenge: 'ch', name: 'Phone' })
  assert.equal(/** @type {any} */ (out).token, 'bds_4')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/mfa/passkey/register/options')
  assert.deepEqual(m.calls[0].body, { challenge: 'ch' })
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/mfa/passkey/register/verify')
  assert.equal(m.calls[1].body.challenge, 'ch')
  assert.equal(m.calls[1].body.name, 'Phone')
})

test('list, rename and remove', async () => {
  const p = { id: 'p1', name: 'Laptop', created_at: 'x', last_used_at: null, transports: [], backup_eligible: false, backup_state: false }
  const m = mockFetch([{ body: { items: [p] } }, { body: { ...p, name: 'Work' } }, { status: 204 }])
  const c = clientWith(m)
  c.proof = { token: 'r1', expires: Date.now() + 60000 }
  assert.deepEqual(await c.auth.passkeys.list(), [p])
  assert.equal((await c.auth.passkeys.rename('p1', 'Work')).name, 'Work')
  assert.deepEqual(m.calls[1].body, { name: 'Work' })
  assert.equal(m.calls[1].method, 'PATCH')
  await c.auth.passkeys.remove('p1')
  assert.equal(m.calls[2].method, 'DELETE')
  assert.equal(m.calls[2].url.pathname, '/v1/acme/_auth/passkeys/p1')
  assert.equal(m.calls[2].headers['X-Backd-Reauth'], 'r1')
})
