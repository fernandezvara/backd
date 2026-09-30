import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, AuthenticationError, NetworkError, localStorageStorage, memoryStorage } from '../src/index.js'
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

test('a refused session token expires the session', async () => {
  const m = mockFetch([{ status: 401, body: errorBody('unauthenticated'), headers: { 'WWW-Authenticate': 'Bearer realm="acme", error="invalid_token"' } }])
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
  const m = mockFetch([{ status: 204 }, { status: 204 }, { status: 204 }, { body: { items: sessions } }, { status: 204 }])
  const { c, events } = clientWith(m)
  await c.storage.set('bds_1')
  await c.auth.changePassword({ currentPassword: 'old', newPassword: 'new' })
  assert.deepEqual(m.calls[0].body, { current_password: 'old', new_password: 'new' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/password')
  assert.equal(await c.auth.token(), 'bds_1', 'this session stays valid')

  await c.auth.logoutAll()
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/logout-all')
  assert.equal(await c.auth.token(), null)

  await c.storage.set('bds_2')
  await c.auth.deleteAccount({ password: 'pw' })
  assert.equal(m.calls[2].method, 'DELETE')
  assert.equal(m.calls[2].url.pathname, '/v1/acme/_auth/me')
  assert.deepEqual(m.calls[2].body, { password: 'pw' })
  assert.equal(await c.auth.token(), null)

  await c.storage.set('bds_3')
  assert.deepEqual(await c.auth.sessions(), sessions)
  await c.auth.revokeSession('s/1')
  assert.equal(m.calls[4].url.pathname, '/v1/acme/_auth/sessions/s%2F1')
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
