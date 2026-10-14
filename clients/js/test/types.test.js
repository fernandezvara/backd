import test from 'node:test'
import assert from 'node:assert/strict'
import { createClient } from '../src/index.js'
import { mockFetch } from './helpers.js'

const doc = { id: 'a', _meta: { created_at: '2026-10-01T00:00:00.000Z', updated_at: '2026-10-01T00:00:00.000Z' } }

test('a client with no types option sends no X-Backd-Types header', async () => {
  const f = mockFetch([{ body: doc }])
  const client = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch })
  await client.db('main').collection('notes').get('a')
  assert.equal(f.calls[0].headers['X-Backd-Types'], undefined)
})

test('the types option is sent with every request', async () => {
  const f = mockFetch([{ body: doc }, { body: { items: [], limit: 20, skip: 0, has_more: false } }])
  const client = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch, types: 'abc123' })
  await client.db('main').collection('notes').get('a')
  await client.db('main').collection('notes').list()
  assert.equal(f.calls[0].headers['X-Backd-Types'], 'abc123')
  assert.equal(f.calls[1].headers['X-Backd-Types'], 'abc123')
})

test('stale types are reported once, and the request still succeeds', async () => {
  const stale = { 'X-Backd-Types-Stale': 'true' }
  const f = mockFetch([{ body: doc, headers: stale }, { body: doc, headers: stale }, { body: doc, headers: stale }])
  let reported = 0
  const client = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch, types: 'old', onStaleTypes: () => reported++ })
  const notes = client.db('main').collection('notes')
  assert.equal((await notes.get('a')).id, 'a')
  await notes.get('a', { cache: false })
  await notes.get('a', { cache: false })
  assert.equal(reported, 1)
})

test('without an onStaleTypes handler it warns once on the console', async (t) => {
  const warn = t.mock.method(console, 'warn', () => {})
  const stale = { 'X-Backd-Types-Stale': 'true' }
  const f = mockFetch([{ body: doc, headers: stale }, { body: doc, headers: stale }])
  const client = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch, types: 'old' })
  await client.db('main').collection('notes').get('a')
  await client.db('main').collection('notes').get('a', { cache: false })
  assert.equal(warn.mock.callCount(), 1)
  assert.match(String(warn.mock.calls[0].arguments[0]), /backd types/)
})

test('a client that sent no fingerprint ignores the header', async (t) => {
  const warn = t.mock.method(console, 'warn', () => {})
  const f = mockFetch([{ body: doc, headers: { 'X-Backd-Types-Stale': 'true' } }])
  const client = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch })
  await client.db('main').collection('notes').get('a')
  assert.equal(warn.mock.callCount(), 0)
})
