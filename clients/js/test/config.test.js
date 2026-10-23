import test, { mock } from 'node:test'
import assert from 'node:assert/strict'
import { createClient } from '../src/index.js'
import { mockFetch, errorBody } from './helpers.js'

/** @param {any} values @param {string} version @param {number} [ttl] @param {string} [etag] */
const answer = (values, version, ttl = 300, etag = `W/"${version}"`) => ({ body: { values, version, cache_ttl: ttl }, headers: { ETag: etag } })
const notModified = { status: 304 }

function memoryStore() {
  /** @type {Map<string, string>} */
  const m = new Map()
  return {
    m,
    get length() {
      return m.size
    },
    /** @param {number} i */
    key: (i) => [...m.keys()][i] ?? null,
    /** @param {string} k */
    getItem: (k) => m.get(k) ?? null,
    /** @param {string} k @param {string} v */
    setItem: (k, v) => void m.set(k, v),
    /** @param {string} k */
    removeItem: (k) => void m.delete(k),
  }
}

/**
 * @param {Parameters<typeof mockFetch>[0]} answers
 * @param {{ storage?: any, client?: Record<string, unknown> }} [opts]
 */
function make(answers, opts = {}) {
  const f = mockFetch(answers)
  const client = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch, configStorage: opts.storage ?? false, ...opts.client })
  return { client, f }
}

test('load fetches and caches; a fresh copy costs no request', async () => {
  const { client, f } = make([answer({ maintenance: { enabled: false, message: '' }, features: { export: { enabled: true } } }, 'c1')])
  const values = await client.config.load()
  assert.equal(values.maintenance.enabled, false)
  assert.equal(f.calls.length, 1)
  assert.equal(f.calls[0].url.pathname, '/v1/acme/_config')
  assert.equal(f.calls[0].url.search, '')

  assert.equal(client.config.get('features.export.enabled'), true) // sync: from the copy
  assert.equal(client.config.getSync('maintenance.message'), '')
  assert.equal(client.config.getSync('nope', 'fallback'), 'fallback')
  await client.config.load()
  assert.equal(f.calls.length, 1)
})

test('get fetches the branch of a key that was never loaded, and only then returns a promise', async () => {
  const { client, f } = make([answer({ features: { export: { enabled: true }, reports: { enabled: false } } }, 'c1')])
  const p = client.config.get('features.export.enabled')
  assert.ok(p instanceof Promise)
  assert.equal(await p, true)
  assert.equal(f.calls[0].url.searchParams.get('paths'), 'features')
  assert.equal(client.config.get('features.reports.enabled'), false) // the branch is held now
  assert.deepEqual((await client.config.load(['features'])).features.export, { enabled: true })
  assert.equal(f.calls.length, 1)
})

test('refresh forces a fetch, and a stale copy is served then revalidated with If-None-Match', async () => {
  mock.timers.enable({ apis: ['Date'], now: 1_000_000 })
  try {
    const { client, f } = make([
      answer({ maintenance: { enabled: false } }, 'c1', 60),
      { status: 304, headers: { ETag: 'W/"c1"' } },
      answer({ maintenance: { enabled: true } }, 'c2', 60),
      answer({ maintenance: { enabled: false } }, 'c3', 60),
    ])
    await client.config.load()
    /** @type {string[][]} */
    const changes = []
    client.config.on('change', (paths) => changes.push(paths))

    mock.timers.tick(61_000) // stale
    assert.equal(client.config.get('maintenance.enabled'), false) // served at once
    await new Promise((r) => setImmediate(r))
    assert.equal(f.calls.length, 2)
    assert.equal(f.calls[1].headers['If-None-Match'], 'W/"c1"')
    assert.deepEqual(changes, [])

    // fresh again after the 304
    assert.equal(client.config.get('maintenance.enabled'), false)
    assert.equal(f.calls.length, 2)

    // refresh: true always asks, and a change is announced
    assert.equal(await client.config.get('maintenance.enabled', { refresh: true }), true)
    assert.deepEqual(changes, [['maintenance.enabled']])
    assert.equal(f.calls[2].headers['If-None-Match'], 'W/"c1"')

    // load({refresh}) too
    await client.config.load(undefined, { refresh: true })
    assert.deepEqual(changes[1], ['maintenance.enabled'])
  } finally {
    mock.timers.reset()
  }
})

test('a failed background revalidation keeps serving the copy', async () => {
  mock.timers.enable({ apis: ['Date'], now: 1_000_000 })
  try {
    const { client, f } = make([answer({ a: { b: 1 } }, 'c1', 10), { status: 503, body: errorBody('unavailable') }])
    await client.config.load()
    mock.timers.tick(11_000)
    assert.equal(client.config.get('a.b'), 1)
    await new Promise((r) => setImmediate(r))
    assert.equal(f.calls.length, 2)
    assert.equal(client.config.get('a.b'), 1)
  } finally {
    mock.timers.reset()
  }
})

test('copies persist per realm and user, and are cleared on sign-out', async () => {
  const storage = memoryStore()
  const first = make([answer({ a: { b: 1 } }, 'c1')], { storage })
  await first.client.config.load()
  assert.equal(storage.m.size, 1)
  assert.match([...storage.m.keys()][0], /^backd\.config:acme:/)

  // A new client (a page reload) starts from the stored copy, with no request.
  const second = make([], { storage })
  assert.deepEqual(await second.client.config.load(), { a: { b: 1 } })
  assert.equal(second.f.calls.length, 0)
  assert.equal(second.client.config.getSync('a.b'), 1)

  // Another user's copy is another entry; signing out removes every copy of the realm.
  second.client.config.clear()
  assert.equal(storage.m.size, 0)
})

test('sign-in and sign-out empty the copies', async () => {
  const storage = memoryStore()
  const { client, f } = make(
    [
      answer({ pub: 1 }, 'c1'),
      { body: { token: 'bds_1', expires_at: '2026-10-20T00:00:00.000Z', user: { id: 'u1', email: 'a@example.com', email_verified: true, roles: [], created_at: '2026-01-01T00:00:00.000Z' } } },
      answer({ pub: 1, private: 2 }, 'c2'),
      { status: 204 },
    ],
    { storage },
  )
  await client.config.load()
  assert.equal(client.config.getSync('private'), undefined)
  await client.auth.login({ email: 'a@example.com', password: 'dev-p4ssw0rd!' })
  assert.equal(storage.m.size, 0)
  assert.equal(client.config.getSync('pub'), undefined)
  assert.equal((await client.config.load()).private, 2)
  assert.equal(f.calls[2].headers.Authorization, 'Bearer bds_1')
  assert.equal(storage.m.size, 1)
  await client.auth.logout()
  assert.equal(storage.m.size, 0)
  assert.equal(client.config.getSync('private'), undefined)
})

test('blocked storage falls back to memory', async () => {
  const blocked = {
    getItem() {
      throw new Error('blocked')
    },
    setItem() {
      throw new Error('blocked')
    },
    removeItem() {
      throw new Error('blocked')
    },
  }
  const { client, f } = make([answer({ a: 1 }, 'c1')], { storage: blocked })
  assert.deepEqual(await client.config.load(), { a: 1 })
  assert.equal(client.config.get('a'), 1)
  assert.equal(f.calls.length, 1)
})

/**
 * A client whose fetch serves the configuration and one realtime stream the test writes to.
 * @param {Parameters<typeof mockFetch>[0]} answers
 */
function live(answers) {
  const f = mockFetch(answers)
  /** @type {Array<{ push(text: string): void, end(): void, body: any }>} */
  const streams = []
  const enc = new TextEncoder()
  const fetch = async (/** @type {any} */ input, /** @type {any} */ init = {}) => {
    if (init.method === 'POST' && String(input).endsWith('/_realtime')) {
      /** @type {any} */
      let controller
      const body = new ReadableStream({ start: (c) => (controller = c) })
      init.signal?.addEventListener('abort', () => {
        try {
          controller.error(new Error('aborted'))
        } catch {}
      })
      streams.push({ body: JSON.parse(init.body), push: (t) => controller.enqueue(enc.encode(t)), end: () => controller.close() })
      return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
    }
    return f.fetch(input, init)
  }
  const client = createClient({ url: 'https://api.test', realm: 'acme', fetch, configStorage: false })
  return { client, f, streams }
}

const frame = (/** @type {string} */ event, /** @type {any} */ data) => `event: ${event}\ndata: ${JSON.stringify(data)}\n\n`
const until = async (/** @type {() => boolean} */ cond, what = 'a condition') => {
  for (let i = 0; i < 200 && !cond(); i++) await new Promise((r) => setTimeout(r, 10))
  assert.ok(cond(), 'timed out waiting for ' + what)
}

test('live adds a _config subscription, and a config_changed rereads the branch held and emits change', async () => {
  const { client, f, streams } = live([
    answer({ maintenance: { enabled: false, message: '' } }, 'c1'),
    notModified, // the stream opens: nothing moved while it was not
    answer({ maintenance: { enabled: true, message: '' } }, 'c2'),
  ])
  await client.config.load(['maintenance'])
  const changes = /** @type {string[][]} */ ([])
  client.config.on('change', (paths) => changes.push(paths))
  const stop = client.config.live(['maintenance'])
  await until(() => streams.length === 1, 'the stream')
  assert.deepEqual(streams[0].body.subscriptions, [{ id: '_config', config: { paths: ['maintenance'] } }])

  streams[0].push(frame('ready', { subscriptions: ['_config'] }))
  streams[0].push(frame('config_changed', { id: '_config', version: 'c2', paths: ['maintenance.enabled'] }))
  await until(() => changes.length === 1, 'the change')
  assert.deepEqual(changes[0], ['maintenance.enabled'])
  assert.equal(client.config.getSync('maintenance.enabled'), true)
  assert.equal(f.calls.length, 3) // the load, the check when the stream opened, then the change
  assert.equal(f.calls[2].headers['If-None-Match'], 'W/"c1"')
  stop()
})

test('a change that is none of a held branch costs no request', async () => {
  const { client, f, streams } = live([answer({ maintenance: { enabled: false } }, 'c1'), notModified])
  await client.config.load(['maintenance'])
  client.config.live()
  await until(() => streams.length === 1)
  streams[0].push(frame('ready', {}))
  streams[0].push(frame('config_changed', { id: '_config', version: 'c2', paths: ['features.export.enabled'] }))
  await new Promise((r) => setTimeout(r, 100))
  assert.equal(f.calls.length, 2) // the load and the check when the stream opened, nothing for the change
})

test('an empty list of paths rereads everything held, and so does a reconnection', async () => {
  const { client, f, streams } = live([
    answer({ maintenance: { enabled: false } }, 'c1'),
    notModified,
    answer({ maintenance: { enabled: true } }, 'c2'),
    answer({ maintenance: { enabled: false } }, 'c3'),
  ])
  await client.config.load()
  const seen = /** @type {string[][]} */ ([])
  client.config.on('change', (p) => seen.push(p))
  client.config.live()
  await until(() => streams.length === 1)
  streams[0].push(frame('ready', {}))
  streams[0].push(frame('config_changed', { id: '_config', version: 'c2', paths: [] }))
  await until(() => seen.length === 1)
  assert.equal(f.calls.length, 3)
  // The stream drops and opens again: changes may have been missed, so the copy is revalidated.
  streams[0].end()
  await until(() => streams.length === 2, 'the reconnection')
  streams[1].push(frame('ready', {}))
  await until(() => seen.length === 2, 'the catch-up')
  assert.equal(f.calls.length, 4)
})

test('a refusal of the subscription goes to the error listener and ends live', async () => {
  const f = mockFetch([])
  const fetch = async (/** @type {any} */ input, /** @type {any} */ init = {}) => {
    if (String(input).endsWith('/_realtime')) {
      return new Response(JSON.stringify({ error: { code: 'invalid_query', message: 'invalid subscription', details: [{ path: 'subscriptions[0].config', reason: 'this realm declares no app configuration' }] } }), {
        status: 400,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    return f.fetch(input, init)
  }
  const client = createClient({ url: 'https://api.test', realm: 'acme', fetch, configStorage: false })
  const errors = /** @type {any[]} */ ([])
  client.config.on('error', (e) => errors.push(e))
  client.config.live()
  await until(() => errors.length === 1, 'the error')
  assert.equal(errors[0].code, 'invalid_query')
})

test('stopping live removes the subscription and closes the stream when nothing else uses it', async () => {
  const { client, streams } = live([])
  const stop = client.config.live()
  await until(() => streams.length === 1)
  stop()
  stop() // twice is fine
  await new Promise((r) => setTimeout(r, 50))
  assert.equal(streams.length, 1)
  assert.equal(client.realtime.running, false)
})
