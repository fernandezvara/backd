// Type-level tests, checked by `npm run typecheck` (tsc): the calls below must compile, and each
// line marked with the expect-error directive must fail to compile, or tsc reports it as unused.
// realm.d.ts is what `backd types` generates for internal/typegen/testdata/shop.
import { createClient, expand } from '../../src/index.js'
import type { Realm } from './realm.js'

const client = createClient<Realm>({ url: 'https://api.example.com', realm: 'shop' })
const orders = client.db('main').collection('orders')

export async function typed() {
  const page = await orders.list({ where: { status: 'paid', total: { $gte: 100 } }, orderBy: '-total' })
  const first = page.items[0]
  const status: 'new' | 'paid' | 'shipped' = first.status
  const note: string | null | undefined = first.note
  const city: string | undefined = first.address?.city
  const receipt: { id: string; name: string } | undefined = first.receipt

  const created = await orders.create({ status: 'new', items: [{ sku: 'a', qty: 1 }] })
  await orders.patch(created.id, { total: 12 })
  await orders.list({ where: { $or: [{ status: 'new' }, { 'items.qty': { $gt: 1 } }] }, orderBy: 'status,-total' })

  const placed = await client.db('main').fn('place', { items: ['a'] })
  const id: string = placed.order_id

  // A collection with custom_id: the id is required on create, and only there.
  const invoices = client.db('main').collection('invoices')
  const invoice = await invoices.create({ id: 'INV-2026-0001', customer: 'ada' })
  await invoices.patch(invoice.id, { total: 3 })
  await invoices.replace(invoice.id, { customer: 'ada' })

  const currency: 'eur' | 'usd' | undefined = client.config.get('shop.currency')

  // References are expanded only on request, into _expanded, typed from the collection they lead to.
  const expanded = await orders.get('o_1', { expand: expand(['invoice', ['customer']], 'related') })
  const linked: { id: string; customer?: string } | null | undefined = expanded._expanded?.invoice
  const related = expanded._expanded?.related
  const firstRelated: string | undefined = Array.isArray(related) ? related[0]?.status : related?.items[0]?.status
  const second = Array.isArray(related) ? related[0]?._expanded?.invoice?.customer : undefined

  // Aggregations name only what the collection declares; search is a list sorted by relevance.
  const summary = await orders.aggregate({ groupBy: ['status', 'paid_at:month'], metrics: ['count', 'sum:total', 'max:paid_at'], tz: 'Europe/Madrid', orderBy: '-count', limit: 12 })
  const months: Array<string | number | boolean | null> = summary.groups.map((g) => g.key['paid_at:month'])
  const found = await orders.search('running shoes', { where: { status: 'paid' }, limit: 5 })
  const best: string | undefined = found.items[0]?.status
  return [status, note, city, receipt, id, currency, linked, firstRelated, second, months, best]
}

// @ts-expect-error unknown database
client.db('nope')
// @ts-expect-error unknown collection
client.db('main').collection('ledgers')
// @ts-expect-error a create in a collection with custom ids needs the id
client.db('main').collection('invoices').create({ customer: 'ada' })
// @ts-expect-error a collection that generates its ids takes none
orders.create({ id: 'mine', status: 'new', items: [] })
// @ts-expect-error a status the schema doesn't allow
orders.create({ status: 'refunded', items: [] })
// @ts-expect-error `items` is required
orders.create({ status: 'new' })
// @ts-expect-error unknown field in where
orders.list({ where: { colour: 'red' } })
// @ts-expect-error order_by field that doesn't exist
orders.list({ orderBy: 'colour' })
// @ts-expect-error text operators are for strings
orders.list({ where: { total: { $like: '1%' } } })
// @ts-expect-error input of a function
client.db('main').fn('place', { items: 'a' })
// @ts-expect-error unknown function
client.db('main').fn('refund', {})
// @ts-expect-error unknown configuration path
client.config.get('shop.nope')

// A collection scoped to a team keeps its types.
export const scoped: Promise<{ id: string }> = orders.inTeam('team-1').create({ status: 'new', items: [] })
// @ts-expect-error the scoped collection is typed too
orders.inTeam('team-1').create({ status: 'refunded', items: [] })
// The organizations API: a role is a string, the app narrows it with the realm's OrgRole.
export const myOrgs = client.orgs.signedIn()

// Untyped, nothing changes.
const plain = createClient({ url: 'https://api.example.com', realm: 'shop' })
export const anything = plain.db('whatever').collection('anything').list({ where: { a: 1 } })
// @ts-expect-error a reference the collection doesn't declare
orders.get('o_1').then((o) => o._expanded?.nope)
// @ts-expect-error a field the collection doesn't declare in aggregate.group_by
orders.aggregate({ groupBy: 'note' })
// @ts-expect-error only dates take a bucket
orders.aggregate({ groupBy: 'status:month' })
// @ts-expect-error sums are for the declared metrics
orders.aggregate({ metrics: 'sum:status' })
// @ts-expect-error a collection that declares no aggregate:
client.db('main').collection('invoices').aggregate({ groupBy: 'customer' })
