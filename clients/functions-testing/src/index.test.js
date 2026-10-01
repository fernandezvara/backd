import { callTimedOut, createContext, fakeJob, FunctionError, MemoryStore, resetIds } from './index.js'
import { NotFoundError, VersionMismatchError } from '../../js/src/errors.js'

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
