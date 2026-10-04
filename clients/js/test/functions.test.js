import { test } from 'node:test'
import assert from 'node:assert/strict'

import { BackdError, ConflictError, Job, JobTimeoutError, createClient } from '../src/index.js'
import { mockFetch, errorBody } from './helpers.js'

/** @param {ReturnType<typeof mockFetch>} m */
const db = (m) => createClient({ url: 'http://api.test', realm: 'shop', fetch: m.fetch }).db('orders')

const job = (overrides = {}) => ({
  id: 'job-1',
  function: 'orders/reconcile',
  status: 'queued',
  created_at: '2026-09-29T12:00:00.000Z',
  result: null,
  ...overrides,
})

/**
 * Calls an async function, asserting it really did answer with a Job
 * (every caller here mocks a 202), so tests can use the Job's own
 * methods without `unknown` narrowing noise.
 * @param {ReturnType<typeof mockFetch>} m
 * @returns {Promise<Job>}
 */
async function asyncFn(m) {
  const out = await db(m).fn('reconcile', {})
  assert.ok(out instanceof Job)
  return out
}

test('fn posts to _func, sending input as JSON', async () => {
  const m = mockFetch([{ body: { total: 20 } }])
  const out = await db(m).fn('checkout', { cart: 'c1' })
  assert.deepEqual(out, { total: 20 })
  assert.equal(m.calls[0].method, 'POST')
  assert.equal(m.calls[0].url.pathname, '/v1/shop/orders/_func/checkout')
  assert.deepEqual(m.calls[0].body, { cart: 'c1' })
  assert.equal(m.calls[0].headers['Content-Type'], 'application/json')
})

test('fn with no input sends null, still as JSON', async () => {
  const m = mockFetch([{ body: null }])
  const out = await db(m).fn('ping')
  assert.equal(out, null)
  assert.equal(m.calls[0].body, null)
  assert.equal(m.calls[0].headers['Content-Type'], 'application/json')
})

test('fn sends idempotencyKey as the Idempotency-Key header', async () => {
  const m = mockFetch([{ body: { ok: true } }])
  await db(m).fn('charge', { amount: 100 }, { idempotencyKey: 'order-42', headers: { 'X-Extra': '1' } })
  assert.equal(m.calls[0].headers['Idempotency-Key'], 'order-42')
  assert.equal(m.calls[0].headers['X-Extra'], '1')
})

test('a function error arrives as a BackdError with the function`s own code', async () => {
  const m = mockFetch([{ status: 409, body: errorBody('out_of_stock', 'not enough stock') }])
  await assert.rejects(
    db(m).fn('checkout', {}),
    (/** @type {any} */ err) => err instanceof ConflictError && err.code === 'out_of_stock' && err.message === 'not enough stock',
  )
})

test('fn returns a Job when the function is async (202)', async () => {
  const m = mockFetch([{ status: 202, body: job() }])
  const out = await db(m).fn('reconcile', {})
  assert.ok(out instanceof Job)
  assert.equal(out.id, 'job-1')
  assert.equal(out.function, 'orders/reconcile')
  assert.equal(out.raw.status, 'queued')
})

test('job.status polls GET .../_jobs/{id}', async () => {
  const m = mockFetch([{ status: 202, body: job() }, { body: job({ status: 'running' }) }])
  const j = await asyncFn(m)
  const status = await j.status()
  assert.equal(status, 'running')
  assert.equal(m.calls[1].method, 'GET')
  assert.equal(m.calls[1].url.pathname, '/v1/shop/orders/_jobs/job-1')
  assert.equal(j.raw.status, 'running')
})

test('job.wait polls until done and returns the output', async () => {
  const m = mockFetch([
    { status: 202, body: job() },
    { body: job({ status: 'running' }) },
    { body: job({ status: 'done', result: { status: 'ok', output: { synced: 3 }, duration_ms: 12 } }) },
  ])
  const j = await asyncFn(m)
  const out = await j.wait({ pollIntervalMs: 0 })
  assert.deepEqual(out, { synced: 3 })
  assert.equal(m.calls.length, 3)
})

test('job.wait throws a BackdError matching a sync call for the same outcome', async () => {
  const m = mockFetch([
    { status: 202, body: job() },
    {
      body: job({
        status: 'done',
        result: { status: 'function_error', http_status: 409, code: 'out_of_stock', message: 'no stock', duration_ms: 5 },
      }),
    },
  ])
  const j = await asyncFn(m)
  await assert.rejects(j.wait(), (/** @type {any} */ err) => err instanceof ConflictError && err.code === 'out_of_stock' && err.status === 409)
})

test('job.wait maps a timeout end reason to a BackdError too, not just function_error', async () => {
  const m = mockFetch([
    { status: 202, body: job() },
    { body: job({ status: 'done', result: { status: 'timeout', http_status: 504, code: 'function_timeout', message: 'x', duration_ms: 900000 } }) },
  ])
  const j = await asyncFn(m)
  await assert.rejects(j.wait(), (/** @type {any} */ err) => err instanceof BackdError && err.code === 'function_timeout' && err.status === 504)
})

test('job.wait gives up after its own timeoutMs, leaving the job alone', async () => {
  const m = mockFetch([
    { status: 202, body: job() },
    { body: job({ status: 'running' }) },
  ])
  const j = await asyncFn(m)
  await assert.rejects(j.wait({ pollIntervalMs: 0, timeoutMs: 0 }), (err) => {
    assert.ok(err instanceof JobTimeoutError)
    assert.equal(err.jobId, 'job-1')
    return true
  })
  assert.equal(m.remaining(), 0)
})

test('respondAsync asks for a job with Prefer: respond-async', async () => {
  const job = { id: 'j1', function: 'main/slow', status: 'queued', attempts: 0, created_at: '2026-09-26T12:00:00.000Z', result: null }
  const m = mockFetch([{ status: 202, body: job }, { body: { n: 2 } }])
  const db = createClient({ url: 'http://api.test', realm: 'blog', fetch: m.fetch }).db('main')
  const queued = await db.fn('slow', { n: 1 }, { respondAsync: true, idempotencyKey: 'k1' })
  assert.ok(queued instanceof Job, 'a sync function called with respondAsync resolves to a Job')
  assert.equal(m.calls[0].headers.Prefer, 'respond-async')
  assert.equal(m.calls[0].headers['Idempotency-Key'], 'k1')
  // Without it, no preference is sent and the output comes back directly.
  assert.deepEqual(await db.fn('slow', { n: 1 }), { n: 2 })
  assert.equal(m.calls[1].headers.Prefer, undefined)
})
