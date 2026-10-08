import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { createClient, AuthenticationError, ConflictError, ForbiddenError, ValidationError, BackdError, parseOAuthReturn } from '../src/index.js'
import { challengeOf, newPKCE } from '../src/oauth.js'
import { mockFetch } from './helpers.js'

const memory = () => {
  /** @type {string | null} */
  let held = null
  return { get: () => held, set: (/** @type {string} */ v) => (held = v), remove: () => (held = null) }
}

/** @param {ReturnType<typeof mockFetch>} m @param {object} [extra] */
const clientOf = (m, extra = {}) => createClient({ url: 'https://api.test', realm: 'acme', fetch: m.fetch, oauthStorage: memory(), ...extra })

const session = { token: 'bds_abc', token_type: 'Bearer', session_id: 's1', expires_at: 'x', user: { id: 'u1', email: 'a@x.io', email_verified: true, roles: [], locale: 'en', created_at: 'x' } }

test('PKCE: the challenge is the S256 of the verifier, and verifiers are random', () => {
  const { verifier, challenge } = newPKCE()
  assert.match(verifier, /^[A-Za-z0-9_-]{43}$/)
  assert.equal(challenge, createHash('sha256').update(verifier).digest('base64url'))
  assert.equal(challengeOf('dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk'), 'E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM') // RFC 7636's example
  assert.notEqual(newPKCE().verifier, verifier)
})

test('signInWith starts the flow, keeps the verifier and sends the browser to the provider', async () => {
  const m = mockFetch([{ body: { authorize_url: 'https://accounts.google.com/o?state=s' } }])
  const c = clientOf(m)
  /** @type {string[]} */
  const went = []
  const out = await c.auth.signInWith('google', { redirectTo: 'https://app.test/in', invitation: 'bdi_x', navigate: (u) => void went.push(u) })
  assert.deepEqual(went, ['https://accounts.google.com/o?state=s'])
  assert.equal(out.authorizeUrl, went[0])
  const call = m.calls[0]
  assert.equal(call.method, 'POST')
  assert.equal(call.url.pathname, '/v1/acme/_auth/oauth/google/start')
  assert.equal(call.headers.Authorization, undefined, 'a sign-in sends no credentials')
  assert.equal(call.body.redirect_to, 'https://app.test/in')
  assert.equal(call.body.intent, 'signin')
  assert.equal(call.body.invitation, 'bdi_x')
  assert.match(call.body.code_challenge, /^[A-Za-z0-9_-]{43}$/)
  assert.equal('locale' in call.body, false)
  await assert.rejects(clientOf(mockFetch([])).auth.signInWith('google'), /redirectTo/)
})

test('completeSignIn trades the code for a session, once, with the verifier it kept', async () => {
  const start = mockFetch([{ body: { authorize_url: 'https://p/x' } }, { body: { ...session, new_user: true, profile: { name: 'Ada Lovelace' } } }])
  const store = memory()
  const c = clientOf(start, { oauthStorage: store })
  await c.auth.signInWith('google', { redirectTo: 'https://app.test/in', navigate: () => {} })
  const challenge = start.calls[0].body.code_challenge
  /** @type {string[]} */
  const events = []
  c.auth.onAuthChange((e) => events.push(e))
  const out = await c.auth.completeSignIn('https://app.test/in?code=the-code')
  assert.equal(/** @type {any} */ (out)?.new_user, true)
  assert.equal(/** @type {any} */ (out).profile.name, 'Ada Lovelace')
  const token = start.calls[1]
  assert.equal(token.url.pathname, '/v1/acme/_auth/oauth/token')
  assert.equal(token.body.code, 'the-code')
  assert.equal(challengeOf(token.body.code_verifier), challenge)
  assert.equal(await c.auth.token(), 'bds_abc')
  assert.deepEqual(events, ['SIGNED_IN'])
  // The verifier is spent: coming back again with a code has nothing to finish it with.
  await assert.rejects(c.auth.completeSignIn('https://app.test/in?code=again'), (/** @type {any} */ e) => e instanceof ValidationError && e.code === 'sign_in_not_started')
  assert.equal(store.get(), null)
})

test('completeSignIn: an ordinary visit has nothing to finish', async () => {
  const c = clientOf(mockFetch([]))
  assert.equal(await c.auth.completeSignIn('https://app.test/in'), null)
  assert.equal(await c.auth.completeSignIn('https://app.test/in?utm=1'), null)
  assert.equal(await c.auth.completeSignIn(undefined), null)
  assert.deepEqual(parseOAuthReturn('https://app.test/?error=cancelled'), { error: 'cancelled' })
  assert.equal(parseOAuthReturn('nonsense'), null)
})

test('completeSignIn turns the error codes into the errors the client throws', async () => {
  const c = clientOf(mockFetch([]))
  /** @type {Array<[string, Function, number]>} */
  const cases = [
    ['cancelled', AuthenticationError, 401],
    ['account_exists', ConflictError, 409],
    ['link_conflict', ConflictError, 409],
    ['signup_closed', ForbiddenError, 403],
    ['email_not_verified', ForbiddenError, 403],
    ['signin_refused', ForbiddenError, 403],
    ['invitation_invalid', ValidationError, 400],
    ['email_required', ValidationError, 400],
    ['provider_error', BackdError, 502],
    ['something_new', BackdError, 502],
  ]
  for (const [code, Class, status] of cases) {
    await assert.rejects(c.auth.completeSignIn(`https://app.test/in?error=${code}`), (/** @type {any} */ e) => e instanceof /** @type {any} */ (Class) && e.status === status && e.code === (code === 'something_new' ? 'provider_error' : code))
  }
  await assert.rejects(c.auth.completeSignIn('https://app.test/in?error=account_exists&provider=google'), (/** @type {any} */ e) => e.details[0].path === 'provider' && e.details[0].reason === 'google')
})

test('linkProvider needs the session and ends with { linked }', async () => {
  const m = mockFetch([{ body: { authorize_url: 'https://p/x' } }])
  const c = clientOf(m)
  await c.auth.linkProvider('apple', { redirectTo: 'https://app.test/in', navigate: () => {} })
  assert.equal(m.calls[0].body.intent, 'link')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/oauth/apple/start')
  assert.deepEqual(await c.auth.completeSignIn('https://app.test/in?linked=apple'), { linked: 'apple' })
})

test('a sign-in kept in the tab survives the navigation: the verifier goes through the storage', async () => {
  const store = memory()
  const a = clientOf(mockFetch([{ body: { authorize_url: 'https://p/x' } }]), { oauthStorage: store })
  await a.auth.signInWith('google', { redirectTo: 'https://app.test/in', navigate: () => {} })
  assert.match(String(store.get()), /verifier/)
  const m = mockFetch([{ body: session }])
  const b = clientOf(m, { oauthStorage: store }) // the page after the redirect: a new client
  await b.auth.completeSignIn('https://app.test/in?code=c')
  assert.equal(m.calls[0].body.code_verifier.length, 43)
  // An attempt that waited longer than the server keeps it is gone.
  const old = memory()
  old.set(JSON.stringify({ verifier: 'v', provider: 'google', intent: 'signin', at: Date.now() - 11 * 60 * 1000 }))
  await assert.rejects(clientOf(mockFetch([]), { oauthStorage: old }).auth.completeSignIn('https://app.test/in?code=c'), (/** @type {any} */ e) => e.code === 'sign_in_not_started')
})

test('cookie sessions ask for a cookie when redeeming', async () => {
  const m = mockFetch([{ body: { session_id: 's1', expires_at: 'x', user: session.user, new_user: false } }])
  const c = clientOf(m, { cookies: true })
  const store = memory()
  store.set(JSON.stringify({ verifier: 'v', provider: 'google', intent: 'signin', at: Date.now() }))
  c.auth._oauthStorage = store
  await c.auth.completeSignIn('https://app.test/in?code=c')
  assert.equal(m.calls[0].body.cookie, true)
  assert.equal(m.calls[0].credentials, 'include')
})

test('unlinkProvider removes a sign-in method; the last one is a conflict', async () => {
  const m = mockFetch([{ status: 204 }, { status: 409, body: { error: { code: 'last_sign_in_method', message: 'only way', request_id: 'r' } } }])
  const c = clientOf(m)
  await c.auth.unlinkProvider('google')
  assert.equal(m.calls[0].method, 'DELETE')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/identities/google')
  await assert.rejects(c.auth.unlinkProvider('password'), (/** @type {any} */ e) => e instanceof ConflictError && e.code === 'last_sign_in_method')
})

test('signInWithIdToken sends the token and the raw nonce, and signs in', async () => {
  const m = mockFetch([{ body: { ...session, new_user: true } }, { status: 401, body: { error: { code: 'invalid_token', message: 'no', request_id: 'r' } } }])
  const c = clientOf(m)
  const s = await c.auth.signInWithIdToken('apple', { idToken: 'eyJ…', nonce: 'raw', authorizationCode: 'abc' })
  assert.equal(s.new_user, true)
  assert.deepEqual(m.calls[0].body, { id_token: 'eyJ…', nonce: 'raw', authorization_code: 'abc' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/oauth/apple/id-token')
  assert.equal(await c.auth.token(), 'bds_abc')
  await assert.rejects(c.auth.signInWithIdToken('apple', { idToken: 'bad', nonce: 'raw' }), (/** @type {any} */ e) => e instanceof AuthenticationError && e.code === 'invalid_token')
})

test('linkProviderWithIdToken links to the signed-in user', async () => {
  const m = mockFetch([{ body: { linked: 'google' } }])
  const c = clientOf(m, { storage: { get: () => 'bds_have', set() {}, remove() {} } })
  assert.deepEqual(await c.auth.linkProviderWithIdToken('google', { idToken: 't', nonce: 'n' }), { linked: 'google' })
  assert.equal(m.calls[0].body.intent, 'link')
  assert.equal(m.calls[0].headers.Authorization, 'Bearer bds_have')
})
