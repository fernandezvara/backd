import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, memoryStorage } from '../src/index.js'
import { RevalidationCache } from '../src/cache.js'
import { mockFetch, session } from './helpers.js'

/** @param {number} version */
const meta = (version) => ({ created_at: '2026-09-26T12:00:00.000Z', updated_at: '2026-09-26T12:00:00.000Z', version })
/** @param {string} id @param {number} [version] */
const doc = (id, version = 1) => ({ id, _meta: meta(version), title: 't' + version })
/** @param {object} body @param {string} etag */
const tagged = (body, etag) => ({ body, headers: { ETag: etag } })
/** @param {string} etag */
const notModified = (etag) => ({ status: 304, headers: { ETag: etag } })

/** @param {ReturnType<typeof mockFetch>} m @param {object} [extra] */
const make = (m, extra = {}) => createClient({ url: 'http://api.test', realm: 'blog', fetch: m.fetch, ...extra })
/** @param {{ headers: Record<string, string> }} call @param {string} name */
const header = (call, name) => Object.entries(call.headers).find(([k]) => k.toLowerCase() === name.toLowerCase())?.[1]

test('a second read sends If-None-Match and a 304 returns the body held', async () => {
  const m = mockFetch([tagged(doc('a'), '"1"'), notModified('"1"')])
  const posts = make(m).db('main').collection('posts')
  const first = await posts.get('a')
  assert.equal(header(m.calls[0], 'If-None-Match'), undefined)
  const second = await posts.get('a')
  assert.equal(header(m.calls[1], 'If-None-Match'), '"1"')
  assert.deepEqual(second, first)
  assert.notEqual(second, first, 'each read gets its own copy to change')
})

test('a changed document replaces the entry', async () => {
  const m = mockFetch([tagged(doc('a'), '"1"'), tagged(doc('a', 2), '"2"'), notModified('"2"')])
  const c = make(m)
  const posts = c.db('main').collection('posts')
  await posts.get('a')
  assert.equal((await posts.get('a'))._meta.version, 2)
  assert.equal((await posts.get('a'))._meta.version, 2)
  assert.equal(header(m.calls[2], 'If-None-Match'), '"2"')
  assert.deepEqual(c.cacheStats(), { hits: 1, misses: 2, entries: 1, bytes: c.cacheStats().bytes })
})

test('lists revalidate by their weak tag, per query', async () => {
  const page = { items: [doc('a')], limit: 20, skip: 0, has_more: false }
  const m = mockFetch([tagged(page, 'W/"abc"'), tagged({ ...page, items: [] }, 'W/"def"'), notModified('W/"abc"')])
  const posts = make(m).db('main').collection('posts')
  await posts.list()
  await posts.list({ where: { a: 1 } })
  assert.equal(header(m.calls[1], 'If-None-Match'), undefined, 'another query has its own entry')
  assert.deepEqual(await posts.list(), page)
  assert.equal(header(m.calls[2], 'If-None-Match'), 'W/"abc"')
})

test('a write drops the document entry', async () => {
  const m = mockFetch([tagged(doc('a'), '"1"'), tagged(doc('a', 2), '"2"'), tagged(doc('a', 2), '"2"')])
  const posts = make(m).db('main').collection('posts')
  await posts.get('a')
  await posts.patch('a', { title: 'x' })
  await posts.get('a')
  assert.equal(header(m.calls[2], 'If-None-Match'), undefined)
})

test('a user change clears the cache', async () => {
  const storage = memoryStorage()
  storage.set('tok-ada')
  const m = mockFetch([tagged(doc('a'), '"1"'), { body: session('tok-bob') }, tagged(doc('a'), '"1"')])
  const c = make(m, { storage })
  const posts = c.db('main').collection('posts')
  await posts.get('a')
  assert.equal(c.cacheStats().entries, 1)
  await c.auth.signedIn(/** @type {any} */ (session('tok-bob'))) // as after a login
  assert.equal(c.cacheStats().entries, 0)
  await posts.get('a')
  assert.equal(header(m.calls[1], 'If-None-Match'), undefined, 'bob never revalidates ada\'s copy')
})

test('a different token empties the cache even without sign-in events', async () => {
  const storage = memoryStorage()
  storage.set('one')
  const m = mockFetch([tagged(doc('a'), '"1"'), tagged(doc('a'), '"1"')])
  const posts = make(m, { storage }).db('main').collection('posts')
  await posts.get('a')
  storage.set('two')
  await posts.get('a')
  assert.equal(header(m.calls[1], 'If-None-Match'), undefined)
})

test('cache: false, per client and per call', async () => {
  let m = mockFetch([tagged(doc('a'), '"1"'), tagged(doc('a'), '"1"')])
  let posts = make(m, { cache: false }).db('main').collection('posts')
  await posts.get('a')
  await posts.get('a')
  assert.equal(header(m.calls[1], 'If-None-Match'), undefined)

  m = mockFetch([tagged(doc('a'), '"1"'), tagged(doc('a'), '"1"'), notModified('"1"')])
  posts = make(m).db('main').collection('posts')
  await posts.get('a')
  await posts.get('a', { cache: false })
  assert.equal(header(m.calls[1], 'If-None-Match'), undefined)
  await posts.get('a')
  assert.equal(header(m.calls[2], 'If-None-Match'), '"1"')
})

test('file links and no-store answers are never kept', async () => {
  const m = mockFetch([tagged(doc('a'), '"1"'), { body: doc('b'), headers: { ETag: '"1"', 'Cache-Control': 'private, no-store' } }, tagged(doc('b'), '"1"')])
  const c = make(m)
  const posts = c.db('main').collection('posts')
  await posts.get('a', { fileLinks: true })
  assert.equal(c.cacheStats().entries, 0)
  await posts.get('b')
  assert.equal(c.cacheStats().entries, 0)
})

test('a caller\'s own If-None-Match is passed through, 304 included', async () => {
  const m = mockFetch([notModified('"1"')])
  const res = await make(m).request({ method: 'GET', path: ['main', 'posts', 'a'], headers: { 'If-None-Match': '"1"' } })
  assert.equal(res.status, 304)
  assert.equal(res.data, undefined)
})

test('other routes are not cached', async () => {
  const m = mockFetch([tagged({ ok: true }, '"1"'), tagged({ ok: true }, '"1"')])
  const c = make(m)
  await c.request({ method: 'GET', path: ['_auth', 'me'] })
  await c.request({ method: 'GET', path: ['main', '_func', 'x'] })
  assert.equal(c.cacheStats().entries, 0)
})

test('bounds: entries and bytes, least recently used first', () => {
  const c = new RevalidationCache({ maxEntries: 2, maxBytes: 100 })
  /** @param {number} n */
  const entry = (n) => ({ etag: '"1"', text: 'x'.repeat(n), headers: new Headers() })
  c.set('http://h/a', entry(10))
  c.set('http://h/b', entry(10))
  c.get('http://h/a') // a is now the most recent
  c.set('http://h/c', entry(10))
  assert.deepEqual([...c.entries.keys()], ['http://h/a', 'http://h/c'])
  c.set('http://h/big', entry(101))
  assert.equal(c.entries.has('http://h/big'), false, 'a body over the bound is not kept')
  c.set('http://h/d', entry(95))
  assert.deepEqual([...c.entries.keys()], ['http://h/d'])
  assert.equal(c.stats().bytes, 95)
})
