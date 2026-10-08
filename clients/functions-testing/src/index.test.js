import { callTimedOut, createContext, EmailLimitError, emailMessage, fakeEmail, fakeJob, FunctionError, MemoryStore, resetIds } from './index.js'
import { BackdError, NotFoundError, VersionMismatchError } from '../../js/src/errors.js'

// No test-framework dependency, matching the JS client's "no runtime
// dependencies" (these helpers are dev-only, never shipped).
function assert(cond, message = 'assertion failed') {
  if (!cond) throw new Error(message)
}
function assertEquals(got, want, message) {
  const g = JSON.stringify(got)
  const w = JSON.stringify(want)
  if (g !== w) throw new Error(message ?? `got ${g}, want ${w}`)
}
function assertThrows(fn, errClass) {
  try {
    fn()
  } catch (e) {
    if (!(e instanceof errClass)) throw new Error(`expected ${errClass.name}, got ${e}`)
    return
  }
  throw new Error('expected a throw')
}

async function assertRejects(fn, errClass) {
  try {
    await fn()
  } catch (e) {
    if (errClass && !(e instanceof errClass)) {
      throw new Error(`rejected with ${e?.constructor?.name}, want ${errClass.name}`)
    }
    return e
  }
  throw new Error('expected a rejection')
}

Deno.test('ctx.input, ctx.user, ctx.secrets, ctx.requestId', () => {
  const { ctx } = createContext({
    input: { cart: 'c1' },
    user: { id: 'u1', email: 'ada@example.com', email_verified: true },
    secrets: { STRIPE_KEY: 'sk_test' },
    requestId: 'req-1',
  })
  assertEquals(ctx.input, { cart: 'c1' })
  assertEquals(ctx.user.email, 'ada@example.com')
  assertEquals(ctx.secrets.STRIPE_KEY, 'sk_test')
  assertEquals(ctx.requestId, 'req-1')
  assertEquals(ctx.idempotencyKey, null)
})

Deno.test('ctx.error makes a FunctionError; a bad status throws instead', () => {
  const { ctx } = createContext()
  const e = ctx.error(409, 'out_of_stock', 'Not enough stock', [{ path: 'items[0]', reason: 'sold out' }])
  assertEquals(e.status, 409)
  assertEquals(e.code, 'out_of_stock')
  assertEquals(e.message, 'Not enough stock')
  assertEquals(e.details, [{ path: 'items[0]', reason: 'sold out' }])
  let threw = false
  try {
    ctx.error(500, 'bad', 'not a 4xx')
  } catch {
    threw = true
  }
  assert(threw, 'a non-4xx status should throw, not return an error')
})

Deno.test('ctx.db: create, get, replace, patch, delete', async () => {
  resetIds()
  const { ctx } = createContext({ user: { id: 'u1', email: 'ada@example.com' } })
  const orders = ctx.db('shop').collection('orders')

  const created = await orders.create({ item: 'widget', qty: 1 })
  assertEquals(created.item, 'widget')
  assertEquals(created._meta.version, 1)
  assertEquals(created._meta.owner, 'user:u1')

  const got = await orders.get(created.id)
  assertEquals(got.qty, 1)

  const replaced = await orders.replace(created.id, { item: 'widget', qty: 2 }, { ifMatch: 1 })
  assertEquals(replaced.qty, 2)
  assertEquals(replaced._meta.version, 2)

  const patched = await orders.patch(created.id, { qty: 3 })
  assertEquals(patched.qty, 3)
  assertEquals(patched.item, 'widget') // untouched fields survive
  assertEquals(patched._meta.version, 3)

  await orders.delete(created.id)
  await assertRejects(() => orders.get(created.id), NotFoundError)
})

Deno.test('ctx.db: If-Match mismatch is a VersionMismatchError, like the real client', async () => {
  const { ctx } = createContext()
  const orders = ctx.db('shop').collection('orders')
  const created = await orders.create({ item: 'widget' })
  await assertRejects(() => orders.replace(created.id, { item: 'x' }, { ifMatch: 99 }), VersionMismatchError)
  // "*" (any current version) always matches.
  const replaced = await orders.replace(created.id, { item: 'y' }, { ifMatch: '*' })
  assertEquals(replaced.item, 'y')
})

Deno.test('ctx.db: patch merges nested objects and null removes a field', async () => {
  const { ctx } = createContext()
  const c = ctx.db('shop').collection('orders')
  const doc = await c.create({ item: 'widget', meta: { color: 'red', size: 'm' } })
  const patched = await c.patch(doc.id, { meta: { color: 'blue' }, item: null })
  assertEquals(patched.meta, { color: 'blue', size: 'm' })
  assertEquals('item' in patched, false)
})

Deno.test('ctx.db: list filters with where, orders, paginates', async () => {
  const { ctx } = createContext()
  const c = ctx.db('shop').collection('orders')
  for (const [item, qty] of [['a', 1], ['b', 2], ['c', 3], ['d', 4]]) {
    await c.create({ item, qty })
  }
  const cheap = await c.list({ where: { qty: { $lt: 3 } } })
  assertEquals(cheap.items.map((d) => d.item).sort(), ['a', 'b'])

  const page = await c.list({ orderBy: '-qty', limit: 2 })
  assertEquals(page.items.map((d) => d.item), ['d', 'c'])
  assertEquals(page.has_more, true)
  // The next page by cursor (the fake's cursor is an offset).
  assertEquals(typeof page.next_cursor, 'string')
  const next = await c.list({ orderBy: '-qty', limit: 2, after: page.next_cursor })
  assertEquals(next.items.map((d) => d.item), ['b', 'a'])
  assertEquals(next.has_more, false)
  assertEquals('next_cursor' in next, false)

  const counted = await c.list({ count: true })
  assertEquals(counted.total, 4)
})

Deno.test('ctx.db: iterate fetches every page', async () => {
  const { ctx } = createContext()
  const c = ctx.db('shop').collection('orders')
  for (let i = 0; i < 5; i++) await c.create({ n: i })
  const seen = []
  for await (const doc of c.iterate({ limit: 2 })) seen.push(doc.n)
  assertEquals(seen.length, 5)
})

Deno.test('ctx.admin is only present when admin: true is simulated', () => {
  assertEquals(createContext().ctx.admin, undefined)
  assert(createContext({ admin: true }).ctx.admin !== undefined)
})

Deno.test('a shared MemoryStore lets several createContext calls see the same data', async () => {
  const store = new MemoryStore()
  store.seed('shop', 'orders', [{ item: 'seeded' }])
  const { ctx: ctx1 } = createContext({ store })
  const found = await ctx1.db('shop').collection('orders').list()
  assertEquals(found.items[0].item, 'seeded')

  const { ctx: ctx2 } = createContext({ store })
  await ctx2.db('shop').collection('orders').create({ item: 'from ctx2' })
  assertEquals(store.all('shop', 'orders').length, 2)
})

Deno.test('ctx.call: faked callees, recorded calls, errors and timeouts', async () => {
  const { ctx, fakeCall, calls } = createContext({ calls: ['send-receipt', 'reserve', 'report', 'slow'] })
  fakeCall('send-receipt', async (input) => ({ sent: true, to: input.to }))
  fakeCall('reserve', () => {
    throw ctx.error(409, 'out_of_stock', 'No stock')
  })
  fakeCall('report', () => fakeJob({ id: 'j1', output: { rows: 3 } }))
  fakeCall('slow', () => {
    throw callTimedOut()
  })

  assertEquals(await ctx.call('send-receipt', { to: 'ana@example.com' }, { idempotencyKey: 'k1' }), { sent: true, to: 'ana@example.com' })
  assertEquals(calls('send-receipt'), [{ input: { to: 'ana@example.com' }, options: { idempotencyKey: 'k1' } }])
  assertEquals(calls('reserve'), [])

  const e = await assertRejects(() => ctx.call('reserve', {}), FunctionError)
  assertEquals([e.status, e.code], [409, 'out_of_stock'])

  const job = await ctx.call('report', {})
  assertEquals(job.id, 'j1')
  assertEquals(await job.wait(), { rows: 3 })

  const t = await assertRejects(() => ctx.call('slow', {}))
  assertEquals([t.status, t.code], [504, 'function_timeout'])
})

Deno.test('ctx.call refuses undeclared and unfaked functions', async () => {
  const { ctx, fakeCall } = createContext({ calls: ['reserve', 'unfaked'] })
  fakeCall('other', () => 1)
  const e = await assertRejects(() => ctx.call('other', {}), FunctionError)
  assertEquals([e.status, e.code], [403, 'call_not_declared'])
  const u = await assertRejects(() => ctx.call('unfaked', {}))
  assert(String(u.message).includes('isn\'t faked'), u.message)
  // Without `calls`, any faked name works.
  const free = createContext()
  free.fakeCall('anything', () => 'ok')
  assertEquals(await free.ctx.call('anything'), 'ok')
})

Deno.test('ctx.email.send records emails and refuses what backd would', async () => {
  const none = createContext({})
  assert(none.ctx.email === undefined, 'ctx.email exists only when the function declares email')

  const { ctx, sentEmails } = createContext({ email: true })
  const job = await ctx.email.send({ to_user: 'u1', kind: 'order-shipped', data: { order_no: 7 } })
  assertEquals(job, { id: 'email-job-1', status: 'queued' })
  // Any recipient is allowed: users, and plain addresses with cc and bcc.
  await ctx.email.send({ to: ['x@example.com'], cc: ['y@example.com'], bcc: ['z@example.com'], kind: 'order-shipped' })
  assertEquals(sentEmails().length, 2)
  assertEquals(sentEmails()[0], { to_user: 'u1', kind: 'order-shipped', data: { order_no: 7 } })

  await assertRejects(() => ctx.email.send({ to_user: 'u1' }), TypeError, 'kind is required')
  await assertRejects(() => ctx.email.send({ kind: 'order-shipped' }), TypeError, 'exactly one')
  await assertRejects(() => ctx.email.send({ kind: 'order-shipped', to_user: 'u1', to: ['x@example.com'] }), TypeError, 'exactly one')
  await assertRejects(() => ctx.email.send({ kind: 'reset-password', to_user: 'u1' }), TypeError, 'backd sends itself')
  await assertRejects(() => ctx.email.send({ kind: 'k', to: [{ email: 'x@example.com' }] }), TypeError, 'email addresses')
  await assertRejects(() => ctx.email.send({ kind: 'k', to: ['nope'] }), TypeError, 'email addresses')
})

Deno.test('ctx.email.send enforces the realm\'s caps', async () => {
  const { ctx, sentEmails } = createContext({ email: { perInvocation: 2, recipientsPerMessage: 3 } })
  await assertRejects(() => ctx.email.send({ kind: 'k', to: ['a@example.com', 'b@example.com'], cc: ['c@example.com', 'd@example.com'] }), TypeError, 'at most 3 recipients')
  await ctx.email.send({ kind: 'k', to: ['a@example.com'] })
  await ctx.email.send({ kind: 'k', to: ['b@example.com'] })
  const err = await ctx.email.send({ kind: 'k', to: ['c@example.com'] }).then(() => null, (e) => e)
  assert(err instanceof EmailLimitError && err.code === 'email_limited' && typeof err.retry_after === 'number', 'the third message is over the invocation cap')
  assertEquals(sentEmails().length, 2)
})

Deno.test('emailMessage has the delivery contract, with overrides', () => {
  const m = emailMessage({ kind: 'reset-password', to: [{ email: 'bob@example.com', name: 'Bob' }] })
  assertEquals(m.kind, 'reset-password')
  assertEquals(m.to[0].name, 'Bob')
  assertEquals(Object.keys(m).sort(), ['bcc', 'cc', 'data', 'from', 'html', 'id', 'kind', 'locale', 'reply_to', 'subject', 'text', 'to'])
})

Deno.test('fakeEmail works on its own', async () => {
  const mail = fakeEmail()
  await mail.send({ to_user: 'u1', kind: 'order-shipped' })
  assertEquals(mail.sent().map((m) => m.kind), ['order-shipped'])
})

Deno.test('ctx.step and ctx.progress are recorded, and refuse what backd refuses', () => {
  const { ctx, steps } = createContext()
  ctx.step('load', { total: 10, message: 'reading' })
  ctx.progress(4)
  ctx.progress(10, 'done reading')
  ctx.step('save')
  assertEquals(steps().map((s) => [s.name, s.total, s.current, s.message]), [
    ['load', 10, 10, 'done reading'],
    ['save', null, 0, null],
  ])
  assertEquals(steps()[0].updates, [{ current: 4, message: null }, { current: 10, message: 'done reading' }])
  assertThrows(() => ctx.step(''), TypeError)
  assertThrows(() => ctx.step('x', { total: -1 }), TypeError)
  assertThrows(() => ctx.progress(Number.NaN), TypeError)
  // Without a step, progress starts one called "progress", as the runner does.
  const fresh = createContext()
  fresh.ctx.progress(3)
  assertEquals(fresh.steps()[0].name, 'progress')
})

// A document's files through ctx.db and ctx.admin.db, as the real client has them.
Deno.test('files: put, get, link and delete on a single field', async () => {
  const { ctx, store } = createContext({ admin: true })
  store.seed('app', 'people', [{ id: 'p1', name: 'Ada' }])
  const files = ctx.db('app').collection('people').files('p1', 'avatar')

  const doc = await files.put(new Uint8Array([1, 2, 3]), { name: '../ada.png', type: 'image/png' })
  assertEquals(doc.avatar.name, 'ada.png')
  assertEquals(doc.avatar.size, 3)
  assertEquals(doc.avatar.type, 'image/png')
  assertEquals(doc.avatar.sha256.length, 64)
  assertEquals(doc._meta.version, 2)
  assertEquals([...(store.fileBytes(doc.avatar.id) ?? [])], [1, 2, 3])

  const got = await files.get()
  assertEquals([...(await got.bytes())], [1, 2, 3])
  assertEquals(got.file.id, doc.avatar.id)
  const link = await files.link()
  assertEquals(link.url.startsWith('https://'), true)

  // A second put replaces the file and drops the old bytes.
  const again = await ctx.admin.db('app').collection('people').files('p1', 'avatar').put('two')
  assertEquals(store.fileBytes(doc.avatar.id), undefined)
  assertEquals(again.avatar.name, 'file')
  assertEquals(await (await files.get(again.avatar.id)).text(), 'two')

  const cleared = await files.delete()
  assertEquals(cleared.avatar, undefined)
  await assertRejects(() => files.get(), NotFoundError)
})

Deno.test('files: a declared field enforces what backd does', async () => {
  const { ctx, store } = createContext()
  store.seed('app', 'claims', [{ id: 'c1' }])
  store.declareFiles('app', 'claims', { receipts: { multiple: true, max_files: 2, max_size: 4, types: ['image/*'] } })
  const receipts = ctx.db('app').collection('claims').files('c1', 'receipts')
  await receipts.put('abc', { type: 'image/png' })
  const two = await receipts.put('def', { type: 'image/jpeg' })
  assertEquals(two.receipts.length, 2)
  assertEquals((await assertRejects(() => receipts.put('ghi', { type: 'image/png' }), BackdError)).code, 'too_many_files')
  await assertRejects(() => receipts.get(), TypeError) // which one?
  await receipts.delete(two.receipts[0].id)
  assertEquals((await assertRejects(() => receipts.put('toolong', { type: 'image/png' }), BackdError)).status, 413)
  assertEquals((await assertRejects(() => receipts.put('abc', { type: 'application/pdf' }), BackdError)).code, 'unsupported_file_type')
  await assertRejects(() => receipts.put('abc', { type: 'image/png', ifMatch: 1 }), VersionMismatchError)
})

// A 20×10 PNG's header: all the fake reads of a picture.
function pngHeader(w, h) {
  const b = new Uint8Array(33)
  b.set([137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 13, 73, 72, 68, 82])
  const v = new DataView(b.buffer)
  v.setUint32(16, w)
  v.setUint32(20, h)
  return b
}

Deno.test('files: declared versions start pending, empty or skipped, and a function can remake, generate and delete them', async () => {
  const { ctx, store } = createContext()
  store.seed('app', 'assets', [{ id: 'a1' }])
  store.declareFiles('app', 'assets', {
    photo: { versions: { thumb: { max_width: 10, max_height: 10, fit: 'cover' }, big: { max_width: 100, writable: true }, mark: { writable: true } } },
  })
  const photo = ctx.db('app').collection('assets').files('a1', 'photo')
  const doc = await photo.put(pngHeader(20, 10), { name: 'a.png', type: 'image/png' })
  assertEquals([doc.photo.width, doc.photo.height], [20, 10])
  assertEquals(doc.photo.versions, { thumb: { status: 'pending' }, big: { status: 'pending' }, mark: { status: 'empty' } })

  const thumb = photo.version('thumb')
  const made = await thumb.regenerate()
  assertEquals([made.status, made.width, made.height], ['ready', 10, 10])
  assertEquals(store.all('app', 'assets')[0].photo.versions.thumb.status, 'ready')
  assertEquals(store.all('app', 'assets')[0]._meta.version, 2) // writing a version doesn't change it

  const generated = await photo.version('big').generate({ max_width: 5, format: 'png' })
  assertEquals([generated.width, generated.height, generated.custom], [5, 3, true])
  assertEquals((await assertRejects(() => thumb.generate({ max_width: 5 }), BackdError)).code, 'version_not_writable')
  assertEquals((await assertRejects(() => photo.version('big').generate({ max_width: 5, fit: 'zoom' }), BackdError)).status, 400)
  assertEquals((await assertRejects(() => photo.version('mark').regenerate(), BackdError)).status, 400)
  await assertRejects(() => photo.version('nope').regenerate(), NotFoundError)

  assertEquals((await thumb.delete()).status, 'pending')
  assertEquals((await photo.version('mark').delete()).status, 'empty')

  // Not a picture: the versions are skipped, and asking for one is a 422.
  store.seed('app', 'assets', [{ id: 'a2' }])
  const notes = ctx.db('app').collection('assets').files('a2', 'photo')
  const text = await notes.put('plain words', { type: 'text/plain' })
  assertEquals(text.photo.versions.thumb, { status: 'skipped', reason: 'not_an_image' })
  assertEquals((await assertRejects(() => notes.version('thumb').regenerate(), BackdError)).code, 'not_an_image')
})
