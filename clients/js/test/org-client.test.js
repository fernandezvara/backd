import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, VerificationRequiredError, localStorageStorage, memoryStorage } from '../src/index.js'
import { mockFetch, session } from './helpers.js'

/** @param {string} token @param {string} org */
const scoped = (token, org) => ({ ...session(token), org })

test('a client made with an org signs in to that organization', async () => {
  const m = mockFetch([{ body: scoped('bds_1', 'o1') }])
  const c = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, org: ' Acme-Corp ' })
  const s = await c.auth.login({ email: 'ada@example.com', password: 'pw' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/orgs/acme-corp/login')
  assert.deepEqual(m.calls[0].body, { email: 'ada@example.com', password: 'pw' }, 'the organization is in the path, not the body')
  assert.equal(s.org, 'o1')
  assert.equal(c.auth.current?.org, 'o1')
})

test('login can name the organization and use an invitation', async () => {
  const m = mockFetch([{ body: scoped('bds_1', 'o1') }])
  const c = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch })
  await c.auth.login({ email: 'bob@example.com', password: 'pw', org: 'o1', invitation: 'bdo_x' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/orgs/o1/login')
  assert.deepEqual(m.calls[0].body, { email: 'bob@example.com', password: 'pw', invitation: 'bdo_x' })
})

test('without an organization the realm routes are used', async () => {
  const m = mockFetch([{ body: session('bds_1') }])
  const c = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch })
  await c.auth.login({ email: 'ada@example.com', password: 'pw' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/login')
})

test('signup, magic links and password resets go to the organization too', async () => {
  const m = mockFetch([
    { status: 201, body: scoped('bds_1', 'o1') },
    { status: 202, body: { request_id: 'r1' } },
    { status: 202, body: { status: 'accepted' } },
  ])
  const c = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, org: 'acme-corp' })
  await c.auth.signup({ email: 'new@example.com', password: 'dev-p4ssw0rd!', invitation: 'bdo_x' })
  await c.auth.magicLink.request('ada@example.com', { redirectTo: 'https://app.test/in' })
  await c.auth.requestPasswordReset({ email: 'ada@example.com' })
  assert.deepEqual(m.calls.map((x) => x.url.pathname), [
    '/v1/acme/_auth/orgs/acme-corp/signup', '/v1/acme/_auth/orgs/acme-corp/magic-link', '/v1/acme/_auth/orgs/acme-corp/reset-password/request',
  ])
  for (const call of m.calls) assert.equal(call.body.org, undefined, 'no org field in a body')
})

test('options say what an organization offers', async () => {
  const m = mockFetch([{ body: { items: ['google'], methods: ['password', 'magic_link'] } }, { body: { items: ['google'] } }])
  const c = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, org: 'acme-corp' })
  assert.deepEqual(await c.auth.options(), { providers: ['google'], methods: ['password', 'magic_link'] })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/orgs/acme-corp/providers')
  assert.deepEqual(await c.auth.providers({ org: 'other' }), ['google'])
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/orgs/other/providers')
})

test('client.org gives one client per organization, each with a token of its own', async () => {
  const m = mockFetch([{ body: scoped('bds_a', 'oa') }, { body: scoped('bds_b', 'ob') }, { body: { id: 'u1' } }, { body: { id: 'u1' } }])
  const root = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch })
  const a = root.org('Acme')
  const b = root.org('beta')
  assert.equal(root.org('acme'), a, 'the same client for the same organization')
  assert.equal(a.org('acme'), a)
  assert.equal(a.org('beta'), b, 'from any of them')
  await a.auth.login({ email: 'ada@example.com', password: 'pw' })
  await b.auth.login({ email: 'ada@example.com', password: 'pw' })
  assert.equal(await a.auth.token(), 'bds_a')
  assert.equal(await b.auth.token(), 'bds_b')
  assert.equal(await root.auth.token(), null, 'the client without an organization holds nothing')
  await a.auth.me()
  await b.auth.me()
  assert.equal(m.calls[2].headers.Authorization, 'Bearer bds_a')
  assert.equal(m.calls[3].headers.Authorization, 'Bearer bds_b')
})

test('signing out of one organization leaves the others', async () => {
  const m = mockFetch([{ body: scoped('bds_a', 'oa') }, { body: scoped('bds_b', 'ob') }, { status: 204 }])
  const root = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch })
  const a = root.org('acme')
  const b = root.org('beta')
  await a.auth.login({ email: 'ada@example.com', password: 'pw' })
  await b.auth.login({ email: 'ada@example.com', password: 'pw' })
  await a.auth.logout()
  assert.equal(await a.auth.token(), null)
  assert.equal(await b.auth.token(), 'bds_b')
  assert.deepEqual((await root.orgs.signedIn()).map((x) => x.ref), ['beta'])
})

test('signedIn lists the organizations the app holds a session for', async () => {
  const m = mockFetch([{ body: scoped('bds_a', 'oa') }, { body: scoped('bds_b', 'ob') }, { body: { id: 'u1', org: { id: 'oa', name: 'Acme Corp', slug: 'acme' } } }])
  const root = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch })
  await root.org('acme').auth.login({ email: 'ada@example.com', password: 'pw' })
  await root.org('beta').auth.login({ email: 'ada@example.com', password: 'pw' })
  assert.deepEqual(await root.org('acme').auth.org(), { id: 'oa', name: 'Acme Corp', slug: 'acme' })
  const list = (await root.orgs.signedIn()).sort((x, y) => x.ref.localeCompare(y.ref))
  assert.deepEqual(list.map((x) => [x.ref, x.id, x.name]), [['acme', 'oa', 'Acme Corp'], ['beta', 'ob', undefined]])
  assert.equal(list[0].expiresAt, '2026-10-26T12:00:00.000Z')
})

test('a storage that cannot keep several tokens refuses client.org', () => {
  const root = createClient({ url: 'http://api.test', realm: 'acme', fetch: mockFetch([]).fetch, storage: { get: () => null, set: () => {}, remove: () => {} } })
  assert.throws(() => root.org('acme'), /cannot keep a token per organization/)
  assert.throws(() => createClient({ url: 'http://api.test', realm: 'acme', fetch: mockFetch([]).fetch }).org(' '), /required/)
  const cookies = createClient({ url: 'http://api.test', realm: 'acme', fetch: mockFetch([]).fetch, cookies: true })
  assert.throws(() => cookies.org('acme'), /one session/)
})

test('localStorage keeps a token per organization under its own key', async () => {
  /** @type {Map<string, string>} */
  const store = new Map()
  const ls = {
    getItem: (/** @type {string} */ k) => store.get(k) ?? null,
    setItem: (/** @type {string} */ k, /** @type {string} */ v) => store.set(k, v),
    removeItem: (/** @type {string} */ k) => store.delete(k),
    key: (/** @type {number} */ i) => [...store.keys()][i] ?? null,
    get length() {
      return store.size
    },
  }
  const prev = globalThis.localStorage
  Object.defineProperty(globalThis, 'localStorage', { value: ls, configurable: true })
  try {
    const m = mockFetch([{ body: scoped('bds_a', 'oa') }])
    const root = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, storage: localStorageStorage('t') })
    await root.org('acme').auth.login({ email: 'ada@example.com', password: 'pw' })
    assert.equal(store.get('t:acme'), 'bds_a')
    // A new page: a new client finds the token again.
    const again = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, storage: localStorageStorage('t') })
    assert.equal(await again.org('acme').auth.token(), 'bds_a')
    assert.deepEqual((await again.orgs.signedIn()).map((x) => [x.ref, x.id]), [['acme', 'oa']])
  } finally {
    if (prev === undefined) Reflect.deleteProperty(globalThis, 'localStorage')
    else Object.defineProperty(globalThis, 'localStorage', { value: prev, configurable: true })
  }
})

test('creating an organization signs the app in to it', async () => {
  const m = mockFetch([
    { status: 201, body: { id: 'on', name: 'New', slug: 'new', session: { token: 'bds_n', expires_at: '2026-10-26T12:00:00.000Z', org: 'on' } } },
    { body: { id: 'u1' } },
  ])
  const root = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, storage: memoryStorage() })
  const org = await root.orgs.create({ name: 'New', slug: 'new' })
  assert.equal(org.id, 'on')
  const c = root.org('on')
  assert.equal(await c.auth.token(), 'bds_n')
  await c.auth.me()
  assert.equal(m.calls[1].headers.Authorization, 'Bearer bds_n')
})

test('a new person starts a company', async () => {
  const m = mockFetch([{ status: 201, body: { ...scoped('bds_n', 'on'), organization: { id: 'on', name: 'Start Inc', slug: 'start-inc' } } }])
  const root = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch })
  const out = await root.auth.signUpWithOrg({ email: 'founder@example.com', password: 'dev-p4ssw0rd!', name: 'Start Inc', slug: 'start-inc' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/orgs')
  assert.deepEqual(m.calls[0].body, { email: 'founder@example.com', password: 'dev-p4ssw0rd!', name: 'Start Inc', slug: 'start-inc' })
  assert.equal(out.organization.slug, 'start-inc')
  assert.equal(await root.org('on').auth.token(), 'bds_n', 'the token is the new organization client\'s')
})

test('a company that needs its address verified first starts no session', async () => {
  const m = mockFetch([{ status: 202, body: { status: 'verification_required' } }])
  const root = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch })
  await assert.rejects(root.auth.signUpWithOrg({ email: 'f@example.com', password: 'dev-p4ssw0rd!', name: 'S', slug: 's-inc' }), VerificationRequiredError)
  assert.deepEqual(await root.orgs.signedIn(), [])
})

test('findOrgs asks for the email and previewInvitation reads an invitation without a session', async () => {
  const m = mockFetch([{ status: 202, body: { status: 'accepted' } }, { body: { org: { id: 'o1', name: 'Acme' }, roles: ['editor'], teams: [], binding: 'email', expires_at: 'x' } }])
  const c = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, org: 'acme-corp' })
  await c.auth.findOrgs({ email: 'ada@example.com' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/orgs/find')
  assert.equal(m.calls[0].headers.Authorization, undefined)
  const pv = await c.auth.previewInvitation('bdo_x')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/orgs/acme-corp/invitations/preview')
  assert.equal(pv.org.name, 'Acme')
  await assert.rejects(createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch }).auth.previewInvitation('bdo_x'), /organization is required/)
})

test('a sign-in that returns for an organization is kept by that organization\'s client', async () => {
  const m = mockFetch([{ body: scoped('bds_a', 'oa') }])
  const root = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, completeOnLoad: false })
  const pending = await root.auth.oauthStorage()
  pending.set(JSON.stringify({ verifier: 'v', provider: 'google', intent: 'signin', at: Date.now() }))
  const out = await root.auth.completeSignIn('https://app.test/in?backd_code=c1&backd_org=oa')
  assert.equal(/** @type {any} */ (out).org, 'oa')
  assert.equal(await root.org('oa').auth.token(), 'bds_a')
  assert.equal(await root.auth.token(), null)
})
