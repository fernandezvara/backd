import test from 'node:test'
import assert from 'node:assert/strict'
import { createClient, UpgradeRequiredError, BackdError } from '../src/index.js'
import { schemaVersionHeader } from '../src/client.js'
import { mockFetch } from './helpers.js'

const doc = { id: 'a', _meta: { created_at: '2026-10-01T00:00:00.000Z', updated_at: '2026-10-01T00:00:00.000Z', schema_version: 2 } }

test('without schemaVersions no X-Backd-Schema-Version header is sent', async () => {
  const f = mockFetch([{ body: doc }])
  const client = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch })
  await client.db('main').collection('assets').get('a')
  assert.equal(f.calls[0].headers['X-Backd-Schema-Version'], undefined)
})

test('schemaVersions go on every request, sorted', async () => {
  const f = mockFetch([{ body: doc }, { body: { items: [], limit: 20, skip: 0, has_more: false } }, { body: { results: [] } }, { body: doc }])
  const client = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch, schemaVersions: { 'main/pins': 3, 'main/assets': 2 } })
  const assets = client.db('main').collection('assets')
  await assets.get('a')
  await assets.list()
  await client.db('main').batch([{ op: 'create', collection: 'assets', document: { title: 't' } }])
  await assets.replace('a', { title: 't2' }, { ifMatch: 1 })
  for (const call of f.calls) assert.equal(call.headers['X-Backd-Schema-Version'], 'main/assets=2, main/pins=3')
})

test('a malformed schemaVersions is refused when the client is made', () => {
  assert.throws(() => createClient({ url: 'https://api.test', realm: 'acme', schemaVersions: { assets: 1 } }), /schemaVersions/)
  assert.throws(() => createClient({ url: 'https://api.test', realm: 'acme', schemaVersions: { 'main/assets': 0 } }), /schemaVersions/)
  assert.equal(schemaVersionHeader({}), undefined)
})

test('426 is an UpgradeRequiredError with the collection, the minimum and what was declared, and is not retried', async () => {
  const f = mockFetch([
    { status: 426, body: { error: { code: 'upgrade_required', message: 'update the app', collection: 'main/assets', min: 2, declared: 1, request_id: 'r1' } } },
    { body: doc },
  ])
  const client = createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch, schemaVersions: { 'main/assets': 1 }, retry: { attempts: 3 } })
  await assert.rejects(
    () => client.db('main').collection('assets').get('a'),
    (e) => {
      assert.ok(e instanceof UpgradeRequiredError)
      assert.ok(e instanceof BackdError)
      assert.equal(e.status, 426)
      assert.equal(e.code, 'upgrade_required')
      assert.equal(e.collection, 'main/assets')
      assert.equal(e.min, 2)
      assert.equal(e.declared, 1)
      assert.equal(e.requestId, 'r1')
      return true
    },
  )
  assert.equal(f.calls.length, 1, 'a 426 is never retried')
  assert.equal(f.remaining(), 1)
})
