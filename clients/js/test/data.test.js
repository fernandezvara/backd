import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, ifMatchValue, VersionMismatchError } from '../src/index.js'
import { mockFetch, errorBody } from './helpers.js'

const meta = { created_at: '2026-09-26T12:00:00.000Z', updated_at: '2026-09-26T12:00:00.000Z', version: 1 }
/** @param {string} id @param {object} [fields] */
const doc = (id, fields = {}) => ({ id, _meta: meta, ...fields })

/** @param {ReturnType<typeof mockFetch>} m */
const posts = (m) => createClient({ url: 'http://api.test', realm: 'blog', fetch: m.fetch }).db('main').collection('posts')

/** @param {ReturnType<typeof mockFetch>} m */
const db = (m) => createClient({ url: 'http://api.test', realm: 'blog', fetch: m.fetch }).db('main')

test('list sends the query language as the API expects it', async () => {
  const page = { items: [doc('a')], limit: 10, skip: 5, has_more: true, total: 42 }
  const m = mockFetch([{ body: page }, { body: page }, { body: page }])
  const c = posts(m)
  assert.deepEqual(await c.list({ where: { price: { $lt: 20 }, name: 'a' }, orderBy: ['-price', 'name'], limit: 10, skip: 5, count: true }), page)
  const q = m.calls[0].url.searchParams
  assert.equal(m.calls[0].url.pathname, '/v1/blog/main/posts')
  assert.deepEqual(JSON.parse(/** @type {string} */ (q.get('where'))), { price: { $lt: 20 }, name: 'a' })
  assert.equal(q.get('order_by'), '-price,name')
  assert.equal(q.get('limit'), '10')
  assert.equal(q.get('skip'), '5')
  assert.equal(q.get('count'), 'true')

  await c.list()
  assert.equal(m.calls[1].url.search, '', 'no parameters when none are given')
  await c.list({ where: '{"a":1}', orderBy: 'a', count: false })
  assert.equal(m.calls[2].url.searchParams.get('where'), '{"a":1}')
  assert.equal(m.calls[2].url.searchParams.get('count'), null)
})

test('iterate walks every page', async () => {
  const m = mockFetch([
    { body: { items: [doc('a'), doc('b')], limit: 2, skip: 0, has_more: true } },
    { body: { items: [doc('c')], limit: 2, skip: 2, has_more: false } },
  ])
  /** @type {string[]} */
  const ids = []
  for await (const d of posts(m).iterate({ where: { x: 1 }, limit: 2 })) ids.push(d.id)
  assert.deepEqual(ids, ['a', 'b', 'c'])
  assert.deepEqual(m.calls.map((c) => c.url.searchParams.get('skip')), ['0', '2'])
  assert.equal(m.calls[1].url.searchParams.get('where'), '{"x":1}')

  const empty = mockFetch([{ body: { items: [], limit: 100, skip: 0, has_more: false } }])
  for await (const _ of posts(empty).iterate()) assert.fail('no documents expected')
  assert.equal(empty.calls[0].url.searchParams.get('limit'), '100')
})

test('iterate follows the cursor of each page', async () => {
  const m = mockFetch([
    { body: { items: [doc('a'), doc('b')], limit: 2, skip: 0, has_more: true, next_cursor: 'c-1' } },
    { body: { items: [doc('c'), doc('d')], limit: 2, skip: 0, has_more: true, next_cursor: 'c-2' } },
    { body: { items: [doc('e')], limit: 2, skip: 0, has_more: false } },
  ])
  /** @type {string[]} */
  const ids = []
  for await (const d of posts(m).iterate({ orderBy: '-title', limit: 2 })) ids.push(d.id)
  assert.deepEqual(ids, ['a', 'b', 'c', 'd', 'e'])
  assert.deepEqual(m.calls.map((c) => c.url.searchParams.get('after')), [null, 'c-1', 'c-2'])
  assert.deepEqual(m.calls.map((c) => c.url.searchParams.get('skip')), ['0', null, null], 'a cursor replaces the offset')
  assert.equal(m.calls[2].url.searchParams.get('order_by'), '-title')

  // Starting from a cursor, and a list a cursor can't follow stays on offsets.
  const from = mockFetch([{ body: { items: [doc('z')], limit: 100, skip: 0, has_more: false } }])
  for await (const _ of posts(from).iterate({ after: 'start' })) void _
  assert.equal(from.calls[0].url.searchParams.get('after'), 'start')
  assert.equal(from.calls[0].url.searchParams.get('skip'), null)
})

test('list passes after and returns next_cursor', async () => {
  const m = mockFetch([{ body: { items: [doc('a')], limit: 1, skip: 0, has_more: true, next_cursor: 'abc' } }])
  const page = await posts(m).list({ limit: 1, after: 'prev' })
  assert.equal(m.calls[0].url.searchParams.get('after'), 'prev')
  assert.equal(page.next_cursor, 'abc')
})

test('get, create, replace, patch and delete', async () => {
  const m = mockFetch([{ body: doc('a') }, { status: 201, body: doc('b') }, { body: doc('a') }, { body: doc('a') }, { status: 204 }])
  const c = posts(m)
  assert.equal((await c.get('a/b')).id, 'a')
  assert.equal(m.calls[0].url.pathname, '/v1/blog/main/posts/a%2Fb')

  assert.equal((await c.create({ title: 'x' })).id, 'b')
  assert.equal(m.calls[1].method, 'POST')
  assert.deepEqual(m.calls[1].body, { title: 'x' })

  await c.replace('a', { title: 'y' })
  assert.equal(m.calls[2].method, 'PUT')
  assert.equal(m.calls[2].headers['Content-Type'], 'application/json')
  assert.equal(m.calls[2].headers['If-Match'], undefined)

  await c.patch('a', { title: null })
  assert.equal(m.calls[3].method, 'PATCH')
  assert.equal(m.calls[3].headers['Content-Type'], 'application/merge-patch+json')
  assert.deepEqual(m.calls[3].body, { title: null })

  await c.delete('a')
  assert.equal(m.calls[4].method, 'DELETE')
  assert.equal(m.calls[4].body, undefined)
})

test('ifMatch becomes an If-Match header', async () => {
  const m = mockFetch([{ body: doc('a') }, { body: doc('a') }, { status: 204 }, { status: 412, body: errorBody('version_mismatch') }])
  const c = posts(m)
  await c.replace('a', {}, { ifMatch: 3 })
  assert.equal(m.calls[0].headers['If-Match'], '"3"')
  await c.patch('a', {}, { ifMatch: '*', headers: { 'X-Extra': '1' } })
  assert.equal(m.calls[1].headers['If-Match'], '*')
  assert.equal(m.calls[1].headers['X-Extra'], '1')
  await c.delete('a', { ifMatch: 1 })
  assert.equal(m.calls[2].headers['If-Match'], '"1"')
  await assert.rejects(c.patch('a', {}, { ifMatch: 1 }), VersionMismatchError)

  assert.equal(ifMatchValue(7), '"7"')
  assert.equal(ifMatchValue('"1", "2"'), '"1", "2"')
})

test('batch posts to _batch, converting ifMatch on the operations that have it', async () => {
  const results = [doc('a'), { id: 'b' }]
  const m = mockFetch([{ body: { results } }])
  const out = await db(m).batch([
    { op: 'create', collection: 'posts', document: { title: 'x' } },
    { op: 'delete', collection: 'stock', id: 'b', ifMatch: 3 },
  ])
  assert.deepEqual(out, results)
  assert.equal(m.calls[0].method, 'POST')
  assert.equal(m.calls[0].url.pathname, '/v1/blog/main/_batch')
  assert.deepEqual(m.calls[0].body, {
    operations: [
      { op: 'create', collection: 'posts', document: { title: 'x' } },
      { op: 'delete', collection: 'stock', id: 'b', if_match: '"3"' },
    ],
  })
})

test('a failing batch operation rejects with the usual error types', async () => {
  const m = mockFetch([{ status: 412, body: errorBody('version_mismatch') }])
  await assert.rejects(db(m).batch([{ op: 'patch', collection: 'posts', id: 'a', patch: {}, ifMatch: 1 }]), VersionMismatchError)
})

test('writes with ifMatch are retried; without, they are not', async () => {
  const busy = { status: 503, body: errorBody('unavailable'), headers: { 'Retry-After': '0' } }
  const m = mockFetch([busy, { body: doc('a') }, busy])
  const c = createClient({ url: 'http://api.test', realm: 'blog', fetch: m.fetch, retry: { attempts: 1 } }).db('main').collection('posts')
  await c.patch('a', { t: 1 }, { ifMatch: 1 })
  assert.equal(m.calls.length, 2)
  await assert.rejects(c.patch('a', { t: 1 }))
  assert.equal(m.calls.length, 3)
})

test('create and batch take an idempotencyKey, and keyed requests are retried', async () => {
  const busy = { status: 503, body: errorBody('unavailable'), headers: { 'Retry-After': '0' } }
  const m = mockFetch([{ status: 201, body: doc('a') }, busy, { status: 201, body: doc('b') }, { body: { results: [doc('c')] } }, busy])
  const c = createClient({ url: 'http://api.test', realm: 'blog', fetch: m.fetch, retry: { attempts: 1 } }).db('main')
  await c.collection('posts').create({ title: 'x' }, { idempotencyKey: 'order-1' })
  assert.equal(m.calls[0].headers['Idempotency-Key'], 'order-1')

  // The server deduplicates by key, so the client may repeat the request after a 503.
  const created = await c.collection('posts').create({ title: 'y' }, { idempotencyKey: 'order-2' })
  assert.equal(created.id, 'b')
  assert.equal(m.calls.length, 3)
  assert.equal(m.calls[2].headers['Idempotency-Key'], 'order-2')

  await c.batch([{ op: 'create', collection: 'posts', document: { title: 'z' } }], { idempotencyKey: 'batch-1' })
  assert.equal(m.calls[3].headers['Idempotency-Key'], 'batch-1')

  // Without a key a create is not repeated: it might have taken effect.
  await assert.rejects(c.collection('posts').create({ title: 'w' }), (/** @type {any} */ e) => e.status === 503)
  assert.equal(m.calls.length, 5)
  assert.equal(m.calls[4].headers['Idempotency-Key'], undefined)
})
