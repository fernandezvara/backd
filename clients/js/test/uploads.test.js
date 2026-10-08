import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHash } from 'node:crypto'
import { createClient, Sha256, sha256Blob, BackdError, versionStatus } from '../src/index.js'
import { mockFetch } from './helpers.js'

const sha = (/** @type {Uint8Array | string} */ b) => createHash('sha256').update(b).digest('hex')
const info = (upload = 'proxy', extra = {}) => ({ name: 'receipts', upload, download: 'presigned', multiple: true, max_files: 3, max_size: 1000, types: [], ...extra })
const doc = (extra = {}) => ({ id: 'd1', _meta: { created_at: 'x', updated_at: 'x', version: 2 }, ...extra })
const png = () => new File([new Uint8Array([137, 80, 78, 71, 1, 2, 3])], 'my photo.png', { type: 'image/png' })

/** @param {ReturnType<typeof mockFetch>} m */
const people = (m) => createClient({ url: 'https://api.test', realm: 'acme', apiKey: 'bdk_x', fetch: m.fetch }).db('app').collection('people')

test('the incremental SHA-256 agrees with the standard one, whatever the chunking', async () => {
  for (const len of [0, 1, 55, 56, 63, 64, 65, 119, 120, 1000, 100003]) {
    const data = new Uint8Array(len).map((_, i) => (i * 31 + 7) & 255)
    for (const chunk of [1, 7, 64, 1000, len + 1]) {
      const h = new Sha256()
      for (let i = 0; i < len; i += chunk) h.update(data.subarray(i, i + chunk))
      assert.equal(h.hex(), sha(data), `${len} bytes in chunks of ${chunk}`)
    }
  }
  assert.equal(await sha256Blob(new Blob(['abc'])), sha('abc'))
  assert.equal(new Sha256().hex(), sha(''))
})

test('fileField is asked once per field', async () => {
  const m = mockFetch([{ body: info() }])
  const c = people(m)
  assert.equal((await c.fileField('receipts')).upload, 'proxy')
  assert.equal((await c.fileField('receipts')).max_size, 1000)
  assert.equal(m.calls.length, 1)
  assert.equal(m.calls[0].url.pathname, '/v1/acme/app/people/_files/receipts')
})

test('uploadFile to a proxy field sends the bytes through backd', async () => {
  const m = mockFetch([{ body: info() }, { status: 201, body: doc({ receipts: [{ id: 'fl_1' }] }) }])
  /** @type {any[]} */
  const progress = []
  const out = await people(m).uploadFile('d1', 'receipts', png(), { ifMatch: 2, onProgress: (p) => progress.push(p) })
  assert.equal(out.receipts[0].id, 'fl_1')
  const c = m.calls[1]
  assert.equal(c.url.pathname, '/v1/acme/app/people/d1/_files/receipts')
  assert.equal(c.url.searchParams.get('name'), 'my photo.png')
  assert.equal(c.headers['Content-Type'], 'image/png')
  assert.equal(c.headers['If-Match'], '"2"')
  assert.deepEqual(progress, [{ loaded: 7, total: 7 }]) // outside a browser: once, when it is sent
})

test('uploadFile to a direct field declares the file, sends it to the bucket and completes', async () => {
  const link = { upload_id: 'fl_2', upload_token: 'fut_x', expires_at: 'z', method: 'PUT', url: 'https://bucket.test/k?sig=1', headers: { 'Content-Type': 'image/png', 'x-amz-checksum-sha256': 'abc=' }, url_expires_at: 'z' }
  const m = mockFetch([{ body: info('direct') }, { status: 201, body: link }, { status: 200 }, { status: 201, body: doc({ receipts: [{ id: 'fl_2' }] }) }])
  const file = png()
  const out = await people(m).uploadFile('d1', 'receipts', file, { ifMatch: 2 })
  assert.equal(out.receipts[0].id, 'fl_2')
  const [, start, put, complete] = m.calls
  assert.equal(start.url.pathname, '/v1/acme/app/people/d1/_files/receipts/uploads')
  assert.deepEqual(start.body, { name: 'my photo.png', size: 7, type: 'image/png', sha256: sha(new Uint8Array(await file.arrayBuffer())) })
  assert.equal(start.headers['If-Match'], '"2"')
  // The PUT goes to the signed link, with its headers and none of backd's credentials.
  assert.equal(put.method, 'PUT')
  assert.equal(put.url.host, 'bucket.test')
  assert.equal(put.headers['x-amz-checksum-sha256'], 'abc=')
  assert.equal(put.headers.Authorization, undefined)
  assert.equal(put.blob?.size, 7)
  assert.equal(complete.url.pathname, '/v1/acme/app/people/_files/receipts/uploads/fl_2/complete')
  assert.deepEqual(complete.body, { upload_token: 'fut_x' })
})

test('a bucket that refuses the file is an upload_failed error, and nothing is completed', async () => {
  const link = { upload_id: 'fl_2', upload_token: 'fut_x', method: 'PUT', url: 'https://bucket.test/k', headers: {} }
  const m = mockFetch([{ body: info('direct') }, { status: 201, body: link }, { status: 403 }])
  await assert.rejects(people(m).uploadFile('d1', 'receipts', png()), (/** @type {any} */ e) => e instanceof BackdError && e.code === 'upload_failed' && /CORS/.test(e.message))
  assert.equal(m.remaining(), 0)
})

test('prepareUpload for a proxy field answers what to name in the create', async () => {
  const answer = { upload_id: 'fl_3', upload_token: 'fut_y', expires_at: '2026-10-06T11:00:00.000Z', file: { name: 'my photo.png', size: 7, type: 'image/png', sha256: 'ab' } }
  const m = mockFetch([{ body: info() }, { status: 201, body: answer }])
  const up = await people(m).prepareUpload('receipts', png())
  assert.deepEqual(up.ref, { upload: 'fl_3', token: 'fut_y' })
  assert.equal(up.id, 'fl_3')
  assert.equal(up.token, 'fut_y')
  assert.equal(up.expiresAt, answer.expires_at)
  assert.equal(m.calls[1].url.pathname, '/v1/acme/app/people/_files/receipts/uploads')
  assert.equal(m.calls[1].url.searchParams.get('name'), 'my photo.png')
})

test('prepareUpload for a direct field declares, sends and completes', async () => {
  const link = { upload_id: 'fl_4', upload_token: 'fut_z', expires_at: 'e', method: 'PUT', url: 'https://bucket.test/k', headers: { 'Content-Type': 'image/png' } }
  const m = mockFetch([{ body: info('direct') }, { status: 201, body: link }, { status: 200 }, { body: { upload_id: 'fl_4', file: { name: 'n', size: 7, type: 'image/png', sha256: 'ab' } } }])
  const up = await people(m).prepareUpload('receipts', png(), { name: 'renamed.png' })
  assert.equal(up.id, 'fl_4')
  assert.equal(up.file.type, 'image/png')
  assert.equal(m.calls[1].body.name, 'renamed.png')
  assert.equal(m.calls[1].headers['If-Match'], undefined)
})

test('fileUrl returns the link with a camel-cased expiry; downloadFile needs a browser', async () => {
  const m = mockFetch([{ body: { url: 'https://s3/x', expires_at: '2026-10-06T10:05:00.000Z' } }, { body: { url: 'https://s3/y', expires_at: '2026-10-06T10:05:00.000Z' } }])
  const c = people(m)
  assert.deepEqual(await c.fileUrl('d1', 'receipts', 'fl_1'), { url: 'https://s3/x', expiresAt: '2026-10-06T10:05:00.000Z' })
  assert.equal(m.calls[0].url.searchParams.get('link'), 'json')
  await assert.rejects(c.downloadFile('d1', 'receipts', 'fl_1'), /only works in a browser/)
  assert.equal(m.calls.length, 1) // no link was made for nothing

  ;/** @type {any} */ (globalThis).window = { location: { assign: (/** @type {string} */ u) => (assigned = u) } }
  let assigned = ''
  try {
    const l = await c.downloadFile('d1', 'receipts', 'fl_1')
    assert.equal(assigned, 'https://s3/y')
    assert.equal(l.url, 'https://s3/y')
  } finally {
    delete (/** @type {any} */ (globalThis).window)
  }
})

test('fileLinks asks for links on a read and on a list', async () => {
  const m = mockFetch([{ body: doc() }, { body: { items: [], limit: 20, skip: 0, has_more: false } }, { body: doc() }])
  const c = people(m)
  await c.get('d1', { fileLinks: true })
  await c.list({ fileLinks: true })
  await c.get('d1')
  assert.equal(m.calls[0].url.searchParams.get('file_links'), 'true')
  assert.equal(m.calls[1].url.searchParams.get('file_links'), 'true')
  assert.equal(m.calls[2].url.searchParams.has('file_links'), false)
})

test('deleteFile removes one file and refuses to clear by accident', async () => {
  const m = mockFetch([{ body: doc() }])
  await people(m).deleteFile('d1', 'receipts', 'fl_1', { ifMatch: 2 })
  assert.equal(m.calls[0].method, 'DELETE')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/app/people/d1/_files/receipts/fl_1')
  assert.equal(m.calls[0].headers['If-Match'], '"2"')
  await assert.rejects(people(m).deleteFile('d1', 'receipts', /** @type {any} */ (undefined)), TypeError)
})

// A browser reports progress through XMLHttpRequest.
test('in a browser an upload goes through XMLHttpRequest and reports its progress', async () => {
  /** @type {any[]} */
  const sent = []
  class FakeXHR {
    /** @type {any} */
    onload = null
    /** @type {any} */
    method = ''
    url = ''
    upload = /** @type {any} */ ({})
    headers = /** @type {Record<string, string>} */ ({})
    status = 0
    response = /** @type {any} */ (null)
    /** @param {string} m @param {string} u */
    open(m, u) { this.method = m; this.url = u }
    /** @param {string} k @param {string} v */
    setRequestHeader(k, v) { this.headers[k] = v }
    getAllResponseHeaders() { return 'content-type: application/json\r\n' }
    /** @param {any} body */
    send(body) {
      sent.push({ method: this.method, url: this.url, headers: this.headers, body })
      queueMicrotask(() => {
        this.upload.onprogress({ lengthComputable: true, loaded: 3, total: 7 })
        this.upload.onprogress({ lengthComputable: true, loaded: 7, total: 7 })
        this.status = 201
        this.response = new TextEncoder().encode(JSON.stringify(doc({ receipts: [{ id: 'fl_9' }] }))).buffer
        this.onload()
      })
    }
  }
  const realFetch = globalThis.fetch
  // The field's description is a plain fetch; only the upload itself goes through XMLHttpRequest.
  globalThis.fetch = /** @type {any} */ (async () => new Response(JSON.stringify(info()), { headers: { 'Content-Type': 'application/json' } }))
  ;/** @type {any} */ (globalThis).XMLHttpRequest = FakeXHR
  try {
    const c = createClient({ url: 'https://api.test', realm: 'acme', apiKey: 'bdk_x' }).db('app').collection('people')
    /** @type {number[]} */
    const progress = []
    const out = await c.uploadFile('d1', 'receipts', png(), { onProgress: (p) => progress.push(p.loaded) })
    assert.equal(out.receipts[0].id, 'fl_9')
    assert.deepEqual(progress, [3, 7])
    assert.equal(sent[0].method, 'POST')
    assert.equal(sent[0].headers.Authorization, 'Bearer bdk_x')
  } finally {
    delete (/** @type {any} */ (globalThis).XMLHttpRequest)
    globalThis.fetch = realFetch
  }
})

test('fileUrl with a version asks for it, and is null while the version is not ready', async () => {
  const unavailable = {
    status: 404,
    body: { error: { code: 'version_unavailable', message: 'this version of the file is not available', details: [{ path: 'status', reason: 'pending' }] } },
  }
  const m = mockFetch([{ body: { url: 'https://s3/t', expires_at: '2026-10-06T10:05:00.000Z' } }, unavailable, { status: 404, body: { error: { code: 'not_found', message: 'nope' } } }])
  const c = people(m)
  assert.deepEqual(await c.fileUrl('d1', 'photo', 'fl_1', { version: 'thumb' }), { url: 'https://s3/t', expiresAt: '2026-10-06T10:05:00.000Z' })
  assert.equal(m.calls[0].url.searchParams.get('version'), 'thumb')
  assert.equal(m.calls[0].url.searchParams.get('link'), 'json')
  assert.equal(await c.fileUrl('d1', 'photo', 'fl_1', { version: 'thumb' }), null)
  // Any other failure is still an error, and so is a missing version when none was asked.
  await assert.rejects(c.fileUrl('d1', 'photo', 'fl_1', { version: 'thumb' }), (/** @type {any} */ e) => e.status === 404)
})

test('files get and link pass the version; versionStatus reads it from the document', async () => {
  const m = mockFetch([{ body: { url: 'https://s3/t', expires_at: 'x' } }, { bytes: 'ab', headers: { 'content-type': 'image/jpeg' } }])
  const c = people(m)
  await c.files('d1', 'photo').link('fl_1', { version: 'thumb' })
  await c.files('d1', 'photo').get('fl_1', { version: 'thumb' })
  assert.equal(m.calls[0].url.searchParams.get('version'), 'thumb')
  assert.equal(m.calls[1].url.searchParams.get('version'), 'thumb')

  const file = /** @type {import('../src/index.js').FileDetails} */ ({
    id: 'fl_1', name: 'a.jpg', size: 1, type: 'image/jpeg', sha256: 'x', uploaded_at: 'x', width: 400, height: 300,
    versions: { thumb: { status: 'ready', width: 100, height: 75 }, big: { status: 'failed', reason: 'too_large' }, wait: { status: 'pending' } },
  })
  assert.deepEqual(versionStatus(file, 'thumb'), { status: 'ready', ready: true, reason: undefined, width: 100, height: 75 })
  assert.deepEqual(versionStatus(file, 'big'), { status: 'failed', ready: false, reason: 'too_large', width: undefined, height: undefined })
  assert.equal(versionStatus(file, 'wait').ready, false)
  assert.deepEqual(versionStatus(file, 'nope'), { status: 'undeclared', ready: false })
  assert.deepEqual(versionStatus({ ...file, versions: undefined }, 'thumb'), { status: 'undeclared', ready: false })
})

test('a file version can be made again, generated with parameters, or deleted', async () => {
  const state = { status: 'ready', id: 'fv_1', width: 5, height: 3 }
  const m = mockFetch([{ body: state }, { body: { ...state, custom: true } }, { body: { status: 'pending' } }, { body: doc({ photo: { id: 'fl_9', name: 'a.png' } }) }, { body: state }])
  const c = people(m)
  const v = c.files('d1', 'photo').version('thumb', 'fl_1')
  assert.deepEqual(await v.regenerate(), state)
  assert.equal(m.calls[0].method, 'POST')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/app/people/d1/_files/photo/fl_1/versions/thumb')
  assert.equal(m.calls[0].body, undefined)
  assert.equal((await v.generate({ max_width: 5, format: 'png' })).custom, true)
  assert.deepEqual(m.calls[1].body, { max_width: 5, format: 'png' })
  assert.equal((await v.delete()).status, 'pending')
  assert.equal(m.calls[2].method, 'DELETE')
  // Without a file id, the one a single field holds.
  await c.files('d1', 'photo').version('thumb').regenerate()
  assert.equal(m.calls[3].method, 'GET')
  assert.equal(m.calls[4].url.pathname, '/v1/acme/app/people/d1/_files/photo/fl_9/versions/thumb')
})
