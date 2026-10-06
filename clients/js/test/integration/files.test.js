import { test } from 'node:test'
import assert from 'node:assert/strict'
import { BackdError, ValidationError, NotFoundError } from '../../src/index.js'
import { email, filesApiKey, filesClient, password, skip } from './env.js'

/** A signed-in user of the files realm, and their `docs` collection. */
async function setup() {
  const c = filesClient()
  await c.auth.signup({ email: email('files'), password })
  return { c, docs: c.db('app').collection('docs') }
}
const png = (/** @type {number} */ extra = 0) => new File([new Uint8Array([137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82, ...new Array(extra).fill(7)])], 'pic.png', { type: 'image/png' })
const bytesOf = async (/** @type {Blob} */ b) => new Uint8Array(await b.arrayBuffer())
const fetched = async (/** @type {string} */ url) => new Uint8Array(await (await fetch(url)).arrayBuffer())

test('how a field takes files', { skip }, async () => {
  const { docs } = await setup()
  assert.deepEqual(await docs.fileField('video'), { name: 'video', upload: 'direct', download: 'presigned', multiple: false, max_files: 1, max_size: 1048576, types: ['image/png', 'text/plain'] })
  assert.equal((await docs.fileField('gallery')).max_files, 2)
  assert.equal((await docs.fileField('notes')).download, 'proxy')
  await assert.rejects(docs.fileField('nope'), NotFoundError)
})

test('a proxy upload, a link that works without credentials, and a download through backd', { skip }, async () => {
  const { docs } = await setup()
  const doc = await docs.create({ title: 'a' })
  const progress = []
  const file = png(50)
  const out = await docs.uploadFile(doc.id, 'attachment', file, { onProgress: (p) => progress.push(p) })
  assert.equal(out.attachment.name, 'pic.png')
  assert.equal(out.attachment.type, 'image/png')
  assert.equal(out.attachment.size, file.size)
  assert.equal(out._meta.version, 2)
  assert.ok(progress.length >= 1)

  const { url, expiresAt } = await docs.fileUrl(doc.id, 'attachment', out.attachment.id)
  assert.ok(new Date(expiresAt) > new Date())
  assert.deepEqual(await fetched(url), await bytesOf(file)) // no credentials, straight from the bucket
  const got = await docs.files(doc.id, 'attachment').get() // through the redirect
  assert.deepEqual(await got.bytes(), await bytesOf(file))

  // The proxy-mode field streams through backd, with a link that is backd's own.
  const notes = await docs.uploadFile(doc.id, 'notes', new File(['hello, notes'], 'n.txt', { type: 'text/plain' }))
  const link = await docs.fileUrl(doc.id, 'notes', notes.notes.id)
  assert.ok(link.url.startsWith('http://127.0.0.1:18080/v1/files/app/docs/'))
  assert.equal(await (await fetch(link.url)).text(), 'hello, notes')

  // Links on a read, only when asked for.
  assert.equal((await docs.get(doc.id)).attachment.url, undefined)
  assert.ok((await docs.get(doc.id, { fileLinks: true })).attachment.url)
  assert.ok(/** @type {any} */ ((await docs.list({ fileLinks: true })).items.find((d) => d.id === doc.id)).attachment.expires_at)
})

test('uploadFile refuses what the field refuses, and deleteFile removes', { skip }, async () => {
  const { docs } = await setup()
  const doc = await docs.create({ title: 'b' })
  await assert.rejects(docs.uploadFile(doc.id, 'attachment', png(70 * 1024)), (/** @type {any} */ e) => e.status === 413)
  await assert.rejects(docs.uploadFile(doc.id, 'gallery', new File(['plain'], 'p.txt', { type: 'text/plain' })), (/** @type {any} */ e) => e.code === 'unsupported_file_type')
  const one = await docs.uploadFile(doc.id, 'gallery', png(1))
  const two = await docs.uploadFile(doc.id, 'gallery', png(2))
  assert.equal(two.gallery.length, 2)
  await assert.rejects(docs.uploadFile(doc.id, 'gallery', png(3)), (/** @type {any} */ e) => e.code === 'too_many_files')
  const after = await docs.deleteFile(doc.id, 'gallery', one.gallery[0].id, { ifMatch: two._meta.version })
  assert.equal(after.gallery.length, 1)
  await assert.rejects(docs.deleteFile(doc.id, 'gallery', one.gallery[0].id), NotFoundError)
  // Someone else's document is not theirs to touch.
  const other = await setup()
  await assert.rejects(other.docs.uploadFile(doc.id, 'attachment', png()), NotFoundError)
})

test('a direct upload goes straight to the bucket and is verified by backd', { skip }, async () => {
  const { docs } = await setup()
  const doc = await docs.create({ title: 'c' })
  const progress = []
  const file = png(300)
  const out = await docs.uploadFile(doc.id, 'video', file, { onProgress: (p) => progress.push(p) })
  assert.equal(out.video.type, 'image/png')
  assert.equal(out.video.size, file.size)
  assert.equal(out.video.sha256.length, 64)
  assert.deepEqual(await fetched((await docs.fileUrl(doc.id, 'video', out.video.id)).url), await bytesOf(file))
  // What the field refuses is refused before any byte moves.
  await assert.rejects(docs.uploadFile(doc.id, 'video', new File(['x'.repeat(2 * 1024 * 1024)], 'big.txt', { type: 'text/plain' })), (/** @type {any} */ e) => e instanceof ValidationError)
  await assert.rejects(docs.uploadFile(doc.id, 'video', new File(['%PDF-1.4'], 'a.pdf', { type: 'application/pdf' })), (/** @type {any} */ e) => e.code === 'unsupported_file_type')
  // Content that isn't what it says it is: the type is detected, and the object is deleted.
  await assert.rejects(docs.uploadFile(doc.id, 'video', new File([new Uint8Array([0, 1, 2, 3, 255, 254])], 'fake.png', { type: 'image/png' })), (/** @type {any} */ e) => e.code === 'unsupported_file_type')
})

test('files are uploaded before the document exists, then named in the create', { skip }, async () => {
  const { docs } = await setup()
  const a = await docs.prepareUpload('attachment', png(5)) // proxy
  const v = await docs.prepareUpload('video', png(9)) // direct
  assert.ok(a.token.startsWith('fut_') && v.id.startsWith('fl_'))
  const doc = await docs.create({ title: 'd', attachment: a.ref, video: v.ref })
  assert.equal(doc.attachment.id, a.id)
  assert.equal(doc.video.id, v.id)
  // Used once, and only with its token.
  await assert.rejects(docs.create({ title: 'e', attachment: a.ref }), ValidationError)
  const b = await docs.prepareUpload('attachment', png(6))
  await assert.rejects(docs.create({ title: 'e', attachment: { upload: b.id, token: 'fut_wrong' } }), ValidationError)
  // A write to an existing document can name one too.
  const patched = await docs.patch(doc.id, { attachment: b.ref })
  assert.equal(patched.attachment.id, b.id)
  // Another user can't use it.
  const other = await setup()
  const c = await docs.prepareUpload('attachment', png(7))
  await assert.rejects(other.docs.create({ title: 'x', attachment: c.ref }), ValidationError)
})

test('the rules decide who may upload, and a key skips them', { skip }, async () => {
  const anon = filesClient().db('app').collection('docs')
  await assert.rejects(anon.prepareUpload('attachment', png()), (/** @type {any} */ e) => e.status === 401)
  const { docs } = await setup()
  const doc = await docs.create({ title: 'f' })
  const key = filesClient({ apiKey: filesApiKey }).db('app').collection('docs')
  const out = await key.uploadFile(doc.id, 'attachment', png(4))
  assert.ok(out.attachment.id)
  const mine = await docs.get(doc.id)
  assert.equal(mine.attachment.id, out.attachment.id)
  // And the plain error type is the client's own.
  await assert.rejects(docs.uploadFile('nope', 'attachment', png()), (/** @type {any} */ e) => e instanceof BackdError)
})
