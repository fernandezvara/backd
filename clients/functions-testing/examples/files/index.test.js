import { createContext, MemoryStore } from '../../src/index.js'
import { NotFoundError } from '../../../js/src/errors.js'
import { makeThumbnailer } from './thumbnail.js'
import download from './download.js'

function assertEquals(got, want) {
  if (JSON.stringify(got) !== JSON.stringify(want)) throw new Error(`got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`)
}

Deno.test('the thumbnailer stores a smaller image in the reserved field', async () => {
  const store = new MemoryStore()
  store.seed('main', 'people', [{ id: 'p1', name: 'Ada' }])
  const photo = createContext({ store, admin: true }).ctx.admin.db('main').collection('people').files('p1', 'photo')
  await photo.put(new Uint8Array(1000), { type: 'image/png' })

  const seen = []
  const handler = makeThumbnailer(async (bytes, width) => {
    seen.push([bytes.length, width])
    return new Uint8Array(bytes.slice(0, 10))
  })
  const { ctx } = createContext({ store, admin: true, input: { person: 'p1' } })
  const out = await handler(ctx)

  assertEquals(seen, [[1000, 128]])
  assertEquals(out.size, 10)
  const doc = store.all('main', 'people')[0]
  assertEquals(doc.thumbnail.name, 'thumbnail.png')
  assertEquals(doc.thumbnail.type, 'image/png')
  assertEquals(store.fileBytes(doc.thumbnail.id)?.length, 10)

  // Running it again replaces the thumbnail and frees the first one's bytes.
  const first = doc.thumbnail.id
  await handler(createContext({ store, admin: true, input: { person: 'p1' } }).ctx)
  assertEquals(store.fileBytes(first), undefined)
})

Deno.test('the thumbnailer asks for a person, and a person without a photo is a 404', async () => {
  const handler = makeThumbnailer(async (b) => b)
  const bad = await handler(createContext({ admin: true, input: {} }).ctx).catch((e) => e)
  assertEquals([bad.status, bad.code], [400, 'person_required'])
  const store = new MemoryStore().seed('main', 'people', [{ id: 'p2' }])
  const missing = await handler(createContext({ store, admin: true, input: { person: 'p2' } }).ctx).catch((e) => e)
  if (!(missing instanceof NotFoundError)) throw new Error(`expected NotFoundError, got ${missing}`)
})

Deno.test('the download function counts the download and returns the link', async () => {
  const store = new MemoryStore()
  store.seed('main', 'releases', [{ id: 'r1', version: '1.2.0' }])
  const admin = createContext({ store, admin: true }).ctx.admin.db('main').collection('releases')
  store.declareFiles('main', 'releases', { assets: { multiple: true } })
  const withFile = await admin.files('r1', 'assets').put('binary', { name: 'app.zip', type: 'application/zip' })
  const fileId = withFile.assets[0].id

  const call = () => download(createContext({ store, admin: true, input: { release: 'r1', file: fileId } }).ctx)
  const out = await call()
  assertEquals(out.url.includes(fileId), true)
  await call()
  assertEquals(store.all('main', 'releases')[0].downloads, 2)

  const err = await download(createContext({ store, admin: true, input: { release: 'r1' } }).ctx).catch((e) => e)
  assertEquals(err.status, 400)
  const gone = await download(createContext({ store, admin: true, input: { release: 'r1', file: 'fl_nope' } }).ctx).catch((e) => e)
  if (!(gone instanceof NotFoundError)) throw new Error(`expected NotFoundError, got ${gone}`)
})
