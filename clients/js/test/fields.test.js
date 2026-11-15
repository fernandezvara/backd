import test from 'node:test'
import assert from 'node:assert/strict'
import { createClient } from '../src/index.js'
import { mockFetch } from './helpers.js'

const meta = { created_at: '2026-10-01T00:00:00.000Z', updated_at: '2026-10-01T00:00:00.000Z', version: 1 }
/** @param {ReturnType<typeof mockFetch>} m */
const make = (m) => createClient({ url: 'https://api.test', realm: 'acme', fetch: m.fetch })
/** @param {ReturnType<typeof mockFetch>} m */
const posts = (m) => make(m).db('main').collection('posts')

test('fields go on reads as one comma-separated parameter', async () => {
  const m = mockFetch([{ body: { id: 'a', title: 't' } }, { body: { items: [{ id: 'a', title: 't' }], limit: 20, skip: 0, has_more: false } }])
  const p = posts(m)
  const one = await p.get('a', { fields: ['title', 'address.city'] })
  const page = await p.list({ fields: ['title'], limit: 5 })
  assert.equal(m.calls[0].url.searchParams.get('fields'), 'title,address.city')
  assert.equal(m.calls[1].url.searchParams.get('fields'), 'title')
  assert.equal(m.calls[1].url.searchParams.get('limit'), '5')
  assert.equal(one.title, 't')
  assert.equal(page.items[0].id, 'a')
})

test('fields go on the answers of writes too, next to the other options', async () => {
  const doc = { id: 'a', _meta: meta }
  const m = mockFetch([{ body: doc, status: 201 }, { body: doc }, { body: doc }, { body: doc }])
  const p = posts(m)
  await p.create({ title: 't' }, { fields: ['title'], idempotencyKey: 'k1', team: 'blue' })
  await p.replace('a', { title: 't' }, { fields: ['title'], ifMatch: 1 })
  await p.patch('a', { title: 'u' }, { fields: ['_meta.version'], ifMatch: 2 })
  await p.restore('a', { fields: ['title'] })
  assert.equal(m.calls[0].url.searchParams.get('fields'), 'title')
  assert.equal(m.calls[0].url.searchParams.get('team'), 'blue')
  assert.equal(m.calls[0].headers['Idempotency-Key'], 'k1')
  assert.equal(m.calls[1].url.searchParams.get('fields'), 'title')
  assert.equal(m.calls[1].headers['If-Match'], '"1"')
  assert.equal(m.calls[2].url.searchParams.get('fields'), '_meta.version')
  assert.equal(m.calls[3].url.searchParams.get('fields'), 'title')
})

test('without fields nothing is added', async () => {
  const m = mockFetch([{ body: { id: 'a' } }])
  await posts(m).get('a')
  assert.equal(m.calls[0].url.searchParams.has('fields'), false)
})

test('fields must be a non-empty list of paths', async () => {
  const m = mockFetch([])
  await assert.rejects(() => posts(m).get('a', { fields: [] }), /fields/)
  await assert.rejects(() => posts(m).get('a', { fields: ['title', ' '] }), /fields/)
  await assert.rejects(() => posts(m).list({ fields: [] }), /fields/)
  assert.equal(m.calls.length, 0)
})

test('a bad field is the server\'s invalid_query, with the path', async () => {
  const m = mockFetch([{ status: 400, body: { error: { code: 'invalid_query', message: 'invalid query parameters', details: [{ path: 'fields.nope', reason: 'unknown field' }], request_id: 'r' } } }])
  await assert.rejects(() => posts(m).get('a', { fields: ['nope'] }), (/** @type {any} */ e) => e.code === 'invalid_query' && e.details[0].path === 'fields.nope')
})
