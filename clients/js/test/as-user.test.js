import test from 'node:test'
import assert from 'node:assert/strict'
import { createClient } from '../src/index.js'
import { mockFetch } from './helpers.js'

const meta = { created_at: '2026-10-01T00:00:00.000Z', updated_at: '2026-10-01T00:00:00.000Z', version: 1 }
/** @param {string} title */
const doc = (title) => ({ id: 'a', _meta: meta, title })
/** @param {{ headers: Record<string, string> }} call @param {string} name */
const header = (call, name) => Object.entries(call.headers).find(([k]) => k.toLowerCase() === name.toLowerCase())?.[1]

/** @param {ReturnType<typeof mockFetch>} m @param {object} [extra] */
const make = (m, extra = {}) => createClient({ url: 'https://api.test', realm: 'acme', apiKey: 'bdk_secret', fetch: m.fetch, ...extra })

test('as_user sends X-Backd-On-Behalf-Of on data requests and keeps the key', async () => {
  const m = mockFetch([{ body: doc('t') }, { body: doc('t') }])
  const key = make(m)
  const ada = key.as_user('u_ada')
  await ada.db('main').collection('posts').get('a')
  await key.db('main').collection('posts').get('a')
  assert.equal(header(m.calls[0], 'X-Backd-On-Behalf-Of'), 'u_ada')
  assert.equal(header(m.calls[0], 'Authorization'), 'Bearer bdk_secret')
  assert.equal(header(m.calls[1], 'X-Backd-On-Behalf-Of'), undefined, 'the client it came from is not changed')
})

test('as_user clients have a cache each: one user never gets another user\'s body', async () => {
  const m = mockFetch([
    { body: doc('what ada may see'), headers: { ETag: '"1"' } },
    { body: doc('what bob may see'), headers: { ETag: '"1"' } }, // same version tag, different view
  ])
  const key = make(m)
  const ada = key.as_user('u_ada')
  const bob = key.as_user('u_bob')
  assert.notEqual(ada.cache, bob.cache)
  assert.notEqual(ada.cache, key.cache)
  await ada.db('main').collection('posts').get('a')
  const got = await bob.db('main').collection('posts').get('a')
  assert.equal(header(m.calls[1], 'If-None-Match'), undefined, 'bob must not revalidate with ada\'s tag')
  assert.equal(got.title, 'what bob may see')
})

test('as_user needs an API key, cannot be chained and needs an id', () => {
  const m = mockFetch([])
  assert.throws(() => createClient({ url: 'https://api.test', realm: 'acme', fetch: m.fetch }).as_user('u1'), /API key/)
  const ada = make(m).as_user('u_ada')
  assert.throws(() => ada.as_user('u_bob'), /already acts as a user/)
  assert.throws(() => make(m).as_user(''), /user id is required/)
  assert.throws(() => make(m).as_user('   '), /user id is required/)
})

test('an as_user client refuses the admin API and the account routes before sending anything', async () => {
  const m = mockFetch([])
  const ada = make(m).as_user('u_ada')
  await assert.rejects(() => ada.admin.users.list(), /cannot use the admin API/)
  await assert.rejects(() => ada.auth.me(), /cannot use the admin API or the account routes/)
  assert.equal(m.calls.length, 0)
})

test('as_user on a key client with an organization keeps the organization settings', async () => {
  const m = mockFetch([{ body: doc('t') }])
  const ada = make(m, { org: 'acme-corp' }).as_user('u_ada')
  assert.equal(ada.orgRef, 'acme-corp')
  await ada.db('main').collection('posts').get('a')
  assert.equal(header(m.calls[0], 'X-Backd-On-Behalf-Of'), 'u_ada')
})

test('the app configuration of acting clients is kept apart', async () => {
  const key = make(mockFetch([]))
  const [ada, bob, plain] = await Promise.all([key.as_user('u_ada').config.scope(), key.as_user('u_bob').config.scope(), key.config.scope()])
  assert.notEqual(ada, bob)
  assert.notEqual(ada, plain)
})
