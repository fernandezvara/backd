// Type-level tests, checked by `npm run typecheck` (tsc): the calls below must compile, and each
// line marked with the expect-error directive must fail to compile, or tsc reports it as unused.
// realm.d.ts is what `backd types` generates for internal/typegen/testdata/shop.
import { createClient } from '../../src/index.js'
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
  return [status, note, city, receipt, id, currency]
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

// A collection scoped to an organization keeps its types.
export const scoped: Promise<{ id: string }> = orders.inOrg('org-1').create({ status: 'new', items: [] })
// @ts-expect-error the scoped collection is typed too
orders.inOrg('org-1').create({ status: 'refunded', items: [] })
// The organizations API: a role is a string, the app narrows it with the realm's OrgRole.
export const myOrgs = client.orgs.list()

// Untyped, nothing changes.
const plain = createClient({ url: 'https://api.example.com', realm: 'shop' })
export const anything = plain.db('whatever').collection('anything').list({ where: { a: 1 } })
