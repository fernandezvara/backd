import { callTimedOut, createContext, EmailLimitError, emailMessage, fakeEmail, fakeJob, fakePush, fakeTrigger, FunctionError, MemoryStore, resetIds } from './index.js'
import { BackdError, ExpandTooLargeError, InvalidAggregateError, InvalidExpandError, NotFoundError, ValidationError, VersionMismatchError } from '../../js/src/errors.js'

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

Deno.test('fakeEmail skips users without the consent a send requires', async () => {
  const mail = fakeEmail({ consents: { u1: ['marketing'] } })
  assertEquals(await mail.send({ to_user: 'u2', kind: 'news', requires_consent: 'marketing' }), { status: 'no_consent' })
  assertEquals((await mail.send({ to_user: 'u1', kind: 'news', requires_consent: 'marketing' })).status, 'queued')
  assertEquals(mail.sent().length, 1)
  let refused = false
  try {
    await mail.send({ to: ['a@example.org'], kind: 'news', requires_consent: 'marketing' })
  } catch {
    refused = true
  }
  assertEquals(refused, true)
})

Deno.test('fakePush counts the users who lack a required consent', async () => {
  const push = fakePush({ consents: { u1: ['marketing'] } })
  const r = await push.send({ users: ['u1', 'u2'], title: 'News', body: 'Hi', requires_consent: 'marketing' })
  assertEquals([r.users, r.queued, r.no_consent], [2, 1, 1])
  const all = await push.send({ users: ['u1', 'u2'], title: 'News', body: 'Hi' })
  assertEquals([all.queued, all.no_consent], [2, 0])
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

async function pushRefused(push, message, status, code) {
  try {
    await push.send(message)
  } catch (e) {
    assert(e instanceof FunctionError, `a FunctionError, got ${e}`)
    assertEquals([e.status, e.code], [status, code])
    return
  }
  throw new Error(`accepted ${JSON.stringify(message)}`)
}

Deno.test('fakePush records what backd would queue and refuses what it would refuse', async () => {
  const push = fakePush({ kinds: ['order-shipped'] })
  const r = await push.send({ user: 'u1', kind: 'order-shipped', data: { order_no: 'A-1' }, group: 'order-1' })
  assertEquals([r.users, r.queued, r.limited, r.no_devices, r.jobs], [1, 1, 0, 0, 1])
  await push.send({ users: ['u1', 'u2', 'u1'], title: 'Hi', body: 'There' })
  await push.send({ user: 'u1', silent: true, data: { sync: '1' } })
  assertEquals(push.sent().map((m) => m.kind ?? 'raw'), ['order-shipped', 'raw', 'raw'])
  assertEquals((await push.send({ users: ['a', 'b'], title: 'x', body: 'y' })).users, 2)

  await pushRefused(push, { title: 'x', body: 'y' }, 400, 'invalid_push_message') // nobody
  await pushRefused(push, { user: 'a', users: ['b'], title: 'x', body: 'y' }, 400, 'invalid_push_message')
  await pushRefused(push, { user: 'a', kind: 'other' }, 400, 'unknown_push_kind')
  await pushRefused(push, { user: 'a', kind: 'order-shipped', title: 'x' }, 400, 'invalid_push_message')
  await pushRefused(push, { user: 'a', title: 'x' }, 400, 'invalid_push_message') // no body
  await pushRefused(push, { user: 'a', title: 'x', body: 'y', data: { n: 1 } }, 400, 'invalid_push_message')
  await pushRefused(push, { user: 'a', title: 'x', body: 'y', group: 'a b' }, 400, 'invalid_push_message')
  await pushRefused(push, { user: 'a', silent: true }, 400, 'invalid_push_message')
  await pushRefused(push, { user: 'a', silent: true, title: 'x', data: { a: 'b' } }, 400, 'invalid_push_message')
  await pushRefused(push, { audience: 'all', title: 'x', body: 'y' }, 403, 'push_audience_not_declared')
  await pushRefused(push, { user: 'a', title: 'x', body: 'y'.repeat(4100) }, 413, 'payload_too_large')

  const wide = fakePush({ audience: true })
  const all = await wide.send({ audience: { roles: ['member'] }, title: 'x', body: 'y' })
  assertEquals([all.users, all.queued], [null, null])
})

Deno.test('createContext gives ctx.push when asked, and lists what was sent', async () => {
  const { ctx, sentPushes } = createContext({ push: { kinds: ['order-shipped'] } })
  await ctx.push.send({ user: 'u1', kind: 'order-shipped', data: { order_no: 'A-1' } })
  assertEquals(sentPushes().length, 1)
  assertEquals(createContext().ctx.push, undefined)
})

Deno.test('ctx.config reads the values the test gives, and ctx.admin.config changes them', async () => {
  const { ctx, configChanges } = createContext({ config: { features: { export: { enabled: true } }, maintenance: { enabled: false } }, configWrite: true })
  assertEquals(await ctx.config.get('features.export.enabled'), true)
  assertEquals(await ctx.config.get('maintenance'), { enabled: false })
  assertEquals(await ctx.config.get('nope.nothing'), undefined)
  const r = await ctx.admin.config.set({ 'maintenance.enabled': true, 'features.export.enabled': true })
  assertEquals(r.changed, true)
  assertEquals(configChanges(), [{ path: 'maintenance.enabled', old: false, new: true }])
  assertEquals((await ctx.config.all()).maintenance.enabled, true)
  assertEquals((await ctx.admin.config.history())[0].changes[0].path, 'maintenance.enabled')
})

Deno.test('without config the function has no ctx.config, and without configWrite no ctx.admin.config', () => {
  const { ctx } = createContext({ config: {} })
  assertEquals(typeof ctx.config, 'object')
  assertEquals(ctx.admin, undefined)
  assertEquals(createContext().ctx.config, undefined)
})

Deno.test('ctx.admin.orgs: create, claims, members, teams and invitations in memory', async () => {
  const { ctx, orgChanges } = createContext({ admin: true, orgs: true })
  const org = await ctx.admin.orgs.create({ name: 'Paid Co', owner: 'u1', slug: 'paid' })
  assertEquals(org.name, 'Paid Co')
  await assertRejects(() => ctx.admin.orgs.create({ name: 'Other', owner: 'u2', slug: 'paid' }), FunctionError)
  assertEquals((await ctx.admin.orgs.setClaims(org.id, { plan: 'pro' })).claims, { plan: 'pro' })
  await ctx.admin.orgs.addMember(org.id, { user: 'u2', roles: ['editor'] })
  const team = await ctx.admin.orgs.teams.create(org.id, { name: 'Ops', roles: ['editor'] })
  await ctx.admin.orgs.teams.addMember(org.id, team.id, 'u2')
  assertEquals((await ctx.admin.orgs.members(org.id)).items.find((m) => m.user === 'u2').teams, [team.id])
  const inv = await ctx.admin.orgs.invitations.create(org.id, { binding: 'email', email: 'x@example.com', roles: ['editor'] })
  assert(inv.token.startsWith('bdo_') && inv.link.includes(inv.token))
  await assertRejects(() => ctx.admin.orgs.invitations.create(org.id, { binding: 'none', roles: ['owner'] }), FunctionError)
  // The last manager stays, as in the real one.
  const e = await assertRejects(() => ctx.admin.orgs.removeMember(org.id, 'u1'), FunctionError)
  assertEquals(e.code, 'last_org_manager')
  await ctx.admin.orgs.teams.delete(org.id, team.id)
  assertEquals((await ctx.admin.orgs.members(org.id)).items.find((m) => m.user === 'u2').teams, [])
  assertEquals(orgChanges().map((c) => c.action), ['create', 'setClaims', 'addMember', 'teams.create', 'teams.addMember', 'invitations.create', 'teams.delete'])
})

Deno.test('ctx.admin.orgs.teams: grants, re-windows and revokes team roles', async () => {
  const { ctx, orgChanges } = createContext({ admin: true, orgs: { orgs: [{ id: 'o1', name: 'Acme', teams: [{ id: 'sales', name: 'Sales' }], members: [{ user: 'u1' }] }] } })
  const granted = await ctx.admin.orgs.teams.grantRole('o1', 'sales', { user: 'u1', role: 'manager', until: '2030-01-01T00:00:00Z' })
  assertEquals(granted.roles_in_team, ['manager'])
  assertEquals((await ctx.admin.orgs.teams.members('o1', 'sales', { role: 'manager' })).items.map((m) => m.user), ['u1'])
  await ctx.admin.orgs.teams.setRoleWindow('o1', 'sales', { user: 'u1', role: 'manager' })
  await assertRejects(() => ctx.admin.orgs.teams.setRoleWindow('o1', 'sales', { user: 'u1', role: 'delegate' }), FunctionError)
  await ctx.admin.orgs.teams.revokeRole('o1', 'sales', { user: 'u1', role: 'manager' })
  assertEquals((await ctx.admin.orgs.teams.members('o1', 'sales', { role: 'manager' })).items, [])
  assertEquals(orgChanges().map((c) => c.action), ['teams.grantRole', 'teams.setRoleWindow', 'teams.revokeRole'])
})

Deno.test('ctx.admin.orgs.teams.members: lists members and the holders of a team role', async () => {
  const { ctx } = createContext({
    admin: true,
    orgs: {
      orgs: [{
        id: 'o1', name: 'Acme', teams: [{ id: 'sales', name: 'Sales' }],
        members: [{ user: 'u1', teams: ['sales'] }, { user: 'u2', teamRoles: [{ team: 'sales', role: 'manager' }] }, { user: 'u3' }],
      }],
    },
  })
  const all = await ctx.admin.orgs.teams.members('o1', 'sales')
  assertEquals(all.items.map((m) => m.user), ['u1', 'u2'])
  const managers = await ctx.admin.orgs.teams.members('o1', 'sales', { role: 'manager' })
  assertEquals(managers.items.map((m) => [m.user, m.in_team, m.roles_in_team]), [['u2', false, ['manager']]])
})

Deno.test('ctx.admin.orgs: starts from the organizations the test gives, and ctx.user.orgs passes through', async () => {
  const { ctx } = createContext({
    user: { id: 'u1', email: 'a@example.com', roles: [], orgs: { o1: { name: 'Acme', roles: ['owner'], teams: [], claims: { plan: 'pro' }, counts: true } } },
    admin: true,
    orgs: { orgs: [{ id: 'o1', name: 'Acme', claims: { plan: 'pro' }, members: [{ user: 'u1', roles: ['owner'] }] }] },
  })
  assertEquals(ctx.user.orgs.o1.claims.plan, 'pro')
  assertEquals((await ctx.admin.orgs.list()).map((o) => o.id), ['o1'])
  await assertRejects(() => ctx.admin.orgs.get('nope'), FunctionError)
})

Deno.test('expand follows declared references, applies a read predicate and keeps positions', async () => {
  const { ctx, store } = createContext({ user: { id: 'u1', email: 'a@example.com' } })
  store.seed('blog', 'posts', [
    { id: 'p1', title: 'Hello', category: 'c1', tags: ['t1', 'gone', 't3'], _meta: { owner: 'u1' } },
    { id: 'p2', title: 'Bye' },
    { id: 'p3', title: 'Secret tag', tags: ['t3'] },
  ])
  store.seed('blog', 'categories', [{ id: 'c1', name: 'News', parent: 'root' }, { id: 'root', name: 'All' }])
  store.seed('blog', 'tags', [{ id: 't1', name: 'go' }, { id: 't3', name: 'db', secret: true }])
  store.seed('blog', 'profiles', [{ id: 'pr1', name: 'Ada', email: 'a@example.com', _meta: { owner: 'u1' } }])
  store
    .declareReferences('blog', 'posts', {
      category: { collection: 'categories' },
      tags: { collection: 'tags', many: true, max_expand: 2 },
      author: { field: '_meta.owner', collection: 'profiles', key: '_meta.owner' },
    })
    .declareReferences('blog', 'categories', { parent: { collection: 'categories' } })
    .declareReadRule('blog', 'tags', (doc) => doc.secret !== true)

  const posts = ctx.db('blog').collection('posts')
  const post = await posts.get('p1', { expand: 'category.parent(name),tags(name),author(name)' })
  assertEquals(post._expanded.category.name, 'News')
  assertEquals(post._expanded.category._expanded.parent, { id: 'root', name: 'All' })
  assertEquals(post._expanded.tags, { items: [{ id: 't1', name: 'go' }, null], truncated: true })
  assertEquals(post._expanded.author, { id: 'pr1', name: 'Ada' })
  assert(posts.map.get('p1')._expanded === undefined, 'the stored document is not changed')

  // A list: the second post has no references at all.
  const page = await posts.list({ expand: 'category,tags', orderBy: 'id' })
  assertEquals(page.items[1]._expanded, { category: null, tags: [] })

  // A tag the predicate hides is null for ctx.db and there through the administrator.
  assertEquals((await posts.get('p3', { expand: 'tags(name)' }))._expanded.tags, [null])
  const { ctx: adminCtx } = createContext({ admin: true, store })
  const viaAdmin = await adminCtx.admin.db('blog').collection('posts').get('p3', { expand: 'tags(name)' })
  assertEquals(viaAdmin._expanded.tags, [{ id: 't3', name: 'db' }])
  assert((await posts.get('p1'))._expanded === undefined)

  // Wrong expands are backd's errors; the limits can be lowered.
  const e = await assertRejects(() => posts.get('p1', { expand: 'nope' }), InvalidExpandError)
  assertEquals(e.code, 'invalid_expand')
  await assertRejects(() => posts.get('p1', { expand: 'category.parent.parent' }), InvalidExpandError)
  const { ctx: tight } = createContext({ store, expand: { maxDocs: 1 } })
  await assertRejects(() => tight.db('blog').collection('posts').list({ expand: 'category,tags' }), ExpandTooLargeError)
})

Deno.test('a read predicate hides documents from get and list', async () => {
  const { ctx, store } = createContext({ user: { id: 'u1', email: 'a@example.com' } })
  store.seed('app', 'notes', [{ id: 'n1', _meta: { owner: 'u1' } }, { id: 'n2', _meta: { owner: 'u2' } }])
  store.declareReadRule('app', 'notes', (doc, { user }) => doc._meta.owner === user?.id)
  const notes = ctx.db('app').collection('notes')
  assertEquals((await notes.list()).items.map((d) => d.id), ['n1'])
  await assertRejects(() => notes.get('n2'), NotFoundError)
})

Deno.test('aggregate groups, measures and buckets like backd', async () => {
  const { ctx, store } = createContext()
  store.seed('shop', 'orders', [
    { id: 'o1', category: 'shoes', total: 100.1, paid_at: '2026-09-30T22:30:00Z', _meta: { owner: 'u1' } },
    { id: 'o2', category: 'shoes', total: 200.4, paid_at: '2026-09-30T21:30:00Z', _meta: { owner: 'u2' } },
    { id: 'o3', category: 'hats', total: 20, _meta: { owner: 'u1' } },
    { id: 'o4', total: 5 },
  ])
  const orders = ctx.db('shop').collection('orders')
  const res = await orders.aggregate({ groupBy: 'category', metrics: ['count', 'sum:total', 'avg:total', 'max:paid_at', 'count_distinct:_meta.owner'], orderBy: '-count' })
  assertEquals(res.total_groups, 3)
  assertEquals(res.groups[0], { key: { category: 'shoes' }, count: 2, sum_total: '300.5', avg_total: '150.25', max_paid_at: '2026-09-30T22:30:00Z', 'count_distinct__meta.owner': 2 })
  assertEquals(res.groups.map((g) => g.key.category), ['shoes', null, 'hats']) // ties by key, null first

  // Months in Madrid: 22:30Z is already October there (UTC+2 in summer), 21:30Z is not.
  const months = await orders.aggregate({ groupBy: 'paid_at:month', tz: 'Europe/Madrid', orderBy: 'paid_at:month' })
  assertEquals(months.groups.map((g) => [g.key['paid_at:month'], g.count]), [[null, 2], ['2026-09', 1], ['2026-10', 1]])
  // A total over nothing is a count of 0.
  const none = await orders.aggregate({ where: { category: 'socks' } })
  assertEquals(none.groups, [{ key: {}, count: 0 }])
  // Declared collections refuse what backd would.
  store.declareAggregate('shop', 'orders', { group_by: ['category'], metrics: ['total'] })
  await assertRejects(() => orders.aggregate({ groupBy: 'paid_at' }), InvalidAggregateError)
  await assertRejects(() => orders.aggregate({ metrics: 'sum:qty' }), InvalidAggregateError)
  await assertRejects(() => orders.aggregate({ groupBy: 'category:fortnight' }), InvalidAggregateError)
})

Deno.test('search approximates the text index: words, phrases, exclusions and weights', async () => {
  const { ctx, store } = createContext()
  store.seed('blog', 'articles', [
    { id: 'a1', title: 'Running shoes', body: 'Light shoes for the road', status: 'published' },
    { id: 'a2', title: 'Boots', body: 'Heavy boots, also for running shoes', status: 'published' },
    { id: 'a3', title: 'Kids shoes', body: 'small', status: 'draft' },
  ])
  store.declareTextIndex('blog', 'articles', { title: 10, body: 1 })
  const articles = ctx.db('blog').collection('articles')
  const page = await articles.search('running shoes -kids', { where: { status: 'published' } })
  assertEquals(page.items.map((d) => d.id), ['a1', 'a2'])
  assert(page.items[0]._meta.score > page.items[1]._meta.score, 'the weighted title ranks first')
  assertEquals((await articles.search('"running shoes"')).items.map((d) => d.id), ['a1', 'a2'])
  assertEquals((await articles.list({ where: { $search: 'boots' } })).items.map((d) => d.id), ['a2'])
  await assertRejects(() => articles.list({ orderBy: '_relevance' }), ValidationError)
  // The search is also a filter for an aggregation.
  const counted = await articles.aggregate({ where: { $search: 'shoes' }, groupBy: 'status' })
  assertEquals(counted.groups.map((g) => [g.key.status, g.count]), [['draft', 1], ['published', 2]])
})

Deno.test('fakeTrigger builds the input a trigger function receives', () => {
  resetIds()
  const before = { id: 'o1', status: 'draft', total: 10, _meta: { version: 1, updated_at: '2026-10-01T10:00:00.000Z' } }
  const after = { id: 'o1', status: 'paid', total: 10, note: 'x', _meta: { version: 2, updated_at: '2026-10-01T11:00:00.000Z' } }

  // Ids only by default: no bodies, as a trigger without `fields:`.
  const plain = fakeTrigger({ event: 'updated', collection: 'orders', before, after })
  assertEquals(plain, {
    id: plain.id, event: 'updated', collection: 'orders', doc: { id: 'o1', version: 2 }, version: 2,
    changed: ['note', 'status'], depth: 0, root: plain.id, at: '2026-10-01T11:00:00.000Z',
  })
  assert(plain.id.startsWith('evt'), 'a distinct test id')
  assert(!('before' in plain) && !('after' in plain) && !('by' in plain))

  // Paths: id, _meta and the listed fields only.
  const slim = fakeTrigger({ event: 'updated', collection: 'orders', before, after, fields: ['status'], by: 'u_1' })
  assertEquals(slim.before, { id: 'o1', _meta: before._meta, status: 'draft' })
  assertEquals(slim.after, { id: 'o1', _meta: after._meta, status: 'paid' })
  assertEquals(slim.by, 'u_1')

  // Whole documents, and nested paths.
  assertEquals(fakeTrigger({ event: 'updated', before, after, fields: ['*'] }).after, after)
  const nested = fakeTrigger({ event: 'created', after: { id: 'n1', address: { city: 'Madrid', zip: '28001' }, _meta: { version: 1 } }, fields: ['address.city'] })
  assertEquals(nested.after, { id: 'n1', _meta: { version: 1 }, address: { city: 'Madrid' } })
  assertEquals(nested.before, null)
  assertEquals(nested.changed, [])

  // A removal: before only, the version of the last document, nulls in the bodies.
  const gone = fakeTrigger({ event: 'removed', before: after, fields: ['status'] })
  assertEquals(gone.after, null)
  assertEquals(gone.doc, { id: 'o1', version: 2 })
  assertEquals(gone.changed, [])

  // A chained run and a rescan.
  const chained = fakeTrigger({ event: 'created', after: { id: 'c1', _meta: { version: 1 } }, depth: 3, root: 'r-9', rescan: 'run-1' })
  assertEquals([chained.depth, chained.root, chained.rescan], [3, 'r-9', 'run-1'])

  // Too large for a job: the bodies are left out and it says so.
  const huge = fakeTrigger({ event: 'created', after: { id: 'h1', blob: 'x'.repeat(300 * 1024), _meta: { version: 1 } }, fields: ['*'] })
  assertEquals(huge.truncated, true)
  assert(!('before' in huge) && !('after' in huge))
  assertEquals(huge.doc.id, 'h1')
})

Deno.test('fakeTrigger refuses an event that could not happen', () => {
  const doc = { id: 'a', _meta: { version: 1 } }
  assertThrows(() => fakeTrigger({ event: 'inserted', after: doc }), Error)
  assertThrows(() => fakeTrigger({ event: 'created' }), Error)
  assertThrows(() => fakeTrigger({ event: 'created', before: doc, after: doc }), Error)
  assertThrows(() => fakeTrigger({ event: 'removed' }), Error)
  assertThrows(() => fakeTrigger({ event: 'removed', before: doc, after: doc }), Error)
  assertThrows(() => fakeTrigger({ event: 'updated' }), Error)
})

Deno.test('a trigger handler can be tested with fakeTrigger and the fake ctx', async () => {
  // The shape of a real handler: idempotent on the event id, reads what it needs through admin.
  async function onPaid(input, ctx) {
    const seen = ctx.admin.db('main').collection('seen_events')
    if ((await seen.list({ where: { event_id: input.id } })).items.length > 0) return { skipped: true }
    await seen.create({ event_id: input.id, order: input.doc.id })
    return { order: input.doc.id, by: input.by ?? null }
  }
  const { ctx } = createContext({ admin: true })
  const input = fakeTrigger({
    event: 'updated', collection: 'orders', by: 'u_7',
    before: { id: 'o1', status: 'draft', _meta: { version: 1 } },
    after: { id: 'o1', status: 'paid', _meta: { version: 2 } },
  })
  assertEquals(await onPaid(input, ctx), { order: 'o1', by: 'u_7' })
  assertEquals(await onPaid(input, ctx), { skipped: true }, 'the same event twice is one effect')
})
