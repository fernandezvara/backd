import test from 'node:test'
import assert from 'node:assert/strict'
import {
  createClient,
  fillBuckets,
  AggregateNotEnabledError,
  AggregateTooExpensiveError,
  IndexBuildingError,
  InvalidAggregateError,
  SearchNotEnabledError,
  RetryableError,
  ValidationError,
} from '../src/index.js'
import { mockFetch } from './helpers.js'

const client = (/** @type {ReturnType<typeof mockFetch>} */ f) => createClient({ url: 'https://api.test', realm: 'acme', fetch: f.fetch }).db('shop').collection('orders')

test('aggregate sends the declared parameters and returns the groups', async () => {
  const f = mockFetch([{ body: { groups: [{ key: { category: 'shoes', 'paid_at:month': '2026-09' }, count: 2, sum_total: '300.5' }], total_groups: 1 } }])
  const res = await client(f).aggregate({
    where: { status: { $in: ['paid'] } },
    groupBy: ['category', 'paid_at:month'],
    metrics: ['count', 'sum:total'],
    tz: 'Europe/Madrid',
    orderBy: ['-sum_total'],
    limit: 10,
  })
  const url = new URL(f.calls[0].url)
  assert.equal(url.pathname, '/v1/acme/shop/orders/_aggregate')
  assert.equal(url.searchParams.get('group_by'), 'category,paid_at:month')
  assert.equal(url.searchParams.get('metrics'), 'count,sum:total')
  assert.equal(url.searchParams.get('tz'), 'Europe/Madrid')
  assert.equal(url.searchParams.get('order_by'), '-sum_total')
  assert.equal(url.searchParams.get('limit'), '10')
  assert.equal(url.searchParams.get('where'), '{"status":{"$in":["paid"]}}')
  assert.equal(res.groups[0].sum_total, '300.5')
  assert.equal(res.total_groups, 1)

  // A counter: nothing but the path, in the scope of a team.
  const g = mockFetch([{ body: { groups: [{ key: {}, count: 4 }], total_groups: 1 } }])
  await createClient({ url: 'https://api.test', realm: 'acme', fetch: g.fetch }).db('shop').collection('orders').inTeam('t1').aggregate()
  assert.equal(new URL(g.calls[0].url).search, '?team=t1')
})

test('search is a list sorted by relevance with the text as $search', async () => {
  const f = mockFetch([{ body: { items: [], limit: 5, skip: 0, has_more: false } }])
  await client(f).search('red "running shoes" -kids', { where: { status: 'published' }, language: 'es', limit: 5 })
  const q = new URL(f.calls[0].url).searchParams
  assert.deepEqual(JSON.parse(String(q.get('where'))), { status: 'published', $search: 'red "running shoes" -kids' })
  assert.equal(q.get('order_by'), '_relevance')
  assert.equal(q.get('language'), 'es')
  assert.equal(q.get('limit'), '5')
  await assert.rejects(() => client(f).search('  '), TypeError)
})

test('the errors of aggregations and searches are typed', async () => {
  const body = (/** @type {string} */ code, message = code) => ({ error: { code, message, request_id: 'r1' } })
  const f = mockFetch([
    { status: 400, body: body('aggregate_not_enabled') },
    { status: 400, body: body('invalid_aggregate') },
    { status: 422, body: body('aggregate_too_expensive') },
    { status: 400, body: body('search_not_enabled') },
    { status: 503, body: body('index_building'), headers: { 'Retry-After': '5' } },
  ])
  const c = client(f)
  await assert.rejects(() => c.aggregate(), (e) => e instanceof AggregateNotEnabledError && e instanceof ValidationError)
  await assert.rejects(() => c.aggregate({ groupBy: 'x' }), InvalidAggregateError)
  await assert.rejects(() => c.aggregate(), (e) => e instanceof AggregateTooExpensiveError && e.status === 422)
  await assert.rejects(() => c.search('x'), SearchNotEnabledError)
  await assert.rejects(() => c.search('x'), (e) => e instanceof IndexBuildingError && e instanceof RetryableError && e.retryAfter === 5000)
})

test('fillBuckets adds the empty buckets of a chart', () => {
  const groups = [
    { key: { 'paid_at:month': null }, count: 1 },
    { key: { 'paid_at:month': '2026-01' }, count: 3, sum_total: '10' },
    { key: { 'paid_at:month': '2026-03' }, count: 2, sum_total: '4.5' },
  ]
  const filled = fillBuckets(groups, { field: 'paid_at', unit: 'month', from: '2025-12', to: '2026-04', empty: { sum_total: '0' } })
  assert.deepEqual(
    filled.map((g) => [g.key['paid_at:month'], g.count, g.sum_total]),
    [[null, 1, undefined], ['2025-12', 0, '0'], ['2026-01', 3, '10'], ['2026-02', 0, '0'], ['2026-03', 2, '4.5'], ['2026-04', 0, '0']],
  )
  // Days from dates, a year boundary, and ISO weeks (2026-W01 starts on Monday, December 29th 2025).
  const days = fillBuckets([], { field: 'at', unit: 'day', from: new Date('2026-12-30T10:00:00Z'), to: '2027-01-02' })
  assert.deepEqual(days.map((g) => g.key['at:day']), ['2026-12-30', '2026-12-31', '2027-01-01', '2027-01-02'])
  const weeks = fillBuckets([], { field: 'at', unit: 'week', from: '2025-W51', to: '2026-W02' })
  assert.deepEqual(weeks.map((g) => g.key['at:week']), ['2025-W51', '2025-W52', '2026-W01', '2026-W02'])
  const years = fillBuckets([{ key: { 'at:year': '2026' }, count: 1 }], { field: 'at', unit: 'year', from: '2025', to: '2027' })
  assert.deepEqual(years.map((g) => g.count), [0, 1, 0])

  assert.throws(() => fillBuckets([{ key: { category: 'a' }, count: 1 }], { field: 'at', unit: 'day', from: '2026-01-01', to: '2026-01-02' }), TypeError)
  assert.throws(() => fillBuckets([], { field: 'at', unit: 'day', from: '2026-02-01', to: '2026-01-01' }), TypeError)
  assert.throws(() => fillBuckets([], { field: 'at', unit: 'day', from: '2000-01-01', to: '2026-01-01' }), RangeError)
  assert.throws(() => fillBuckets([], { field: 'at', unit: /** @type {any} */ ('fortnight'), from: '2026', to: '2027' }), TypeError)
})
