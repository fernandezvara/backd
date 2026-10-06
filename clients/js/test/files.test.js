import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createClient, NotFoundError, VersionMismatchError } from '../src/index.js'
import { mockFetch, errorBody } from './helpers.js'

/** @param {string} id @param {string} [name] */
const file = (id, name = 'a.png') => ({ id, name, size: 3, type: 'image/png', sha256: 'ab'.repeat(32), uploaded_at: '2026-10-06T10:00:00.000Z' })
const doc = (extra = {}) => ({ id: 'd1', _meta: { created_at: 'x', updated_at: 'x', version: 2 }, title: 't', ...extra })

/** @param {ReturnType<typeof mockFetch>} m */
const files = (m, field = 'avatar') => createClient({ url: 'https://api.test', realm: 'acme', apiKey: 'bdk_x', fetch: m.fetch }).db('app').collection('people').files('d1', field)

test('put sends the bytes with their type and name, and answers the document', async () => {
  const m = mockFetch([{ status: 201, body: doc({ avatar: file('fl_1') }) }])
  const out = await files(m).put(new Uint8Array([1, 2, 3]), { name: 'a.png', type: 'image/png', ifMatch: 2 })
  assert.equal(out.avatar.id, 'fl_1')
  const c = m.calls[0]
  assert.equal(c.method, 'POST')
  assert.equal(c.url.pathname, '/v1/acme/app/people/d1/_files/avatar')
  assert.equal(c.url.searchParams.get('name'), 'a.png')
  assert.deepEqual([...(c.rawBody ?? [])], [1, 2, 3])
  assert.equal(c.headers['Content-Type'], 'image/png')
  assert.equal(c.headers['If-Match'], '"2"')
  assert.equal(c.headers.Authorization, 'Bearer bdk_x')
})

test('put takes a string, and defaults the type', async () => {
  const m = mockFetch([{ status: 201, body: doc() }])
  await files(m, 'notes').put('héllo')
  assert.deepEqual(new TextDecoder().decode(m.calls[0].rawBody), 'héllo')
  assert.equal(m.calls[0].headers['Content-Type'], 'application/octet-stream')
  assert.equal(m.calls[0].url.searchParams.has('name'), false)
})

test('get downloads a file by id, with its bytes and text', async () => {
  const m = mockFetch([{ bytes: 'hello', headers: { 'Content-Type': 'text/plain' } }, { bytes: 'hello' }])
  const got = await files(m, 'manual').get('fl_9')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/app/people/d1/_files/manual/fl_9')
  assert.equal(m.calls[0].headers.Accept, '*/*')
  assert.equal(await got.text(), 'hello')
  const again = await files(m, 'manual').get('fl_9')
  assert.deepEqual([...(await again.bytes())], [...new TextEncoder().encode('hello')])
})

test('without a fileId a single field is looked up in the document', async () => {
  const m = mockFetch([{ body: doc({ avatar: file('fl_1') }) }, { bytes: 'png' }, { body: doc({ avatar: file('fl_1') }) }, { body: { url: 'https://s3/x', expires_at: '2026-10-06T10:05:00.000Z' } }])
  const got = await files(m).get()
  assert.equal(got.file?.id, 'fl_1')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/app/people/d1')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/app/people/d1/_files/avatar/fl_1')
  const link = await files(m).link()
  assert.equal(link.url, 'https://s3/x')
  assert.equal(m.calls[3].url.searchParams.get('link'), 'json')
})

test('without a fileId an empty field is a NotFoundError, and a field with several asks which', async () => {
  const empty = mockFetch([{ body: doc() }])
  await assert.rejects(files(empty).get(), NotFoundError)
  const several = mockFetch([{ body: doc({ avatar: [file('fl_1'), file('fl_2')] }) }])
  await assert.rejects(files(several).get(), /several files/)
})

test('delete removes one file or the whole field', async () => {
  const m = mockFetch([{ body: doc() }, { body: doc() }, { status: 412, body: errorBody('version_mismatch') }])
  await files(m).delete('fl_1')
  assert.equal(m.calls[0].method, 'DELETE')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/app/people/d1/_files/avatar/fl_1')
  await files(m).delete()
  assert.equal(m.calls[1].url.pathname, '/v1/acme/app/people/d1/_files/avatar')
  await assert.rejects(files(m).delete(undefined, { ifMatch: 1 }), VersionMismatchError)
  assert.equal(m.calls[2].headers['If-Match'], '"1"')
})

test('the admin data route has files too', async () => {
  const m = mockFetch([{ status: 201, body: doc() }])
  const c = createClient({ url: 'https://api.test', realm: 'acme', apiKey: 'bdk_x', fetch: m.fetch })
  await c.admin.data('app', 'people').files('d1', 'avatar').put('x', { type: 'text/plain' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/data/app/people/d1/_files/avatar')
})
