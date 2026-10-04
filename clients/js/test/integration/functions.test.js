import { test } from 'node:test'
import assert from 'node:assert/strict'

import { ConflictError, Job } from '../../src/index.js'
import { skip, user } from './env.js'

test('calling a sync function through the client', { skip }, async () => {
  const ada = await user('ada')
  const out = await ada.c.db('app').fn('echo', { n: 3 })
  assert.deepEqual(out, { doubled: 6 })

  await assert.rejects(
    ada.c.db('app').fn('echo', { n: 1, fail: true }),
    (/** @type {any} */ err) => err instanceof ConflictError && err.code === 'out_of_stock' && err.message === 'not enough stock',
  )
})

test('calling an async function returns a Job that resolves the same way a sync call would', { skip }, async () => {
  const ada = await user('ada')
  const out = await ada.c.db('app').fn('echo_async', { n: 4 })
  assert.ok(out instanceof Job)
  assert.equal(out.function, 'app/echo_async')

  const output = await out.wait({ pollIntervalMs: 100, timeoutMs: 30000 })
  assert.deepEqual(output, { doubled: 8 })
  assert.equal(await out.status(), 'done')
})

test('an async function`s own error arrives through job.wait the same way a sync call`s would', { skip }, async () => {
  const ada = await user('ada')
  const job = await ada.c.db('app').fn('echo_async', { n: 1, fail: true })
  assert.ok(job instanceof Job)
  await assert.rejects(
    job.wait({ pollIntervalMs: 100, timeoutMs: 30000 }),
    (/** @type {any} */ err) => err instanceof ConflictError && err.code === 'out_of_stock' && err.status === 409,
  )
})

test('an Idempotency-Key replays the stored response instead of running again', { skip }, async () => {
  const ada = await user('ada')
  const key = 'itest-' + Math.random().toString(36).slice(2)
  const first = await ada.c.db('app').fn('echo', { n: 5 }, { idempotencyKey: key })
  const second = await ada.c.db('app').fn('echo', { n: 5 }, { idempotencyKey: key })
  assert.deepEqual(first, second)
})

test('respondAsync runs a sync function as a job, once, with its own error as the result', { skip }, async () => {
  const ada = await user('ada')
  const db = ada.c.db('app')

  const job = await db.fn('echo', { n: 5 }, { respondAsync: true })
  assert.ok(job instanceof Job, 'a sync function called with respondAsync answers with a Job')
  assert.equal(job.function, 'app/echo')
  assert.deepEqual(await job.wait({ pollIntervalMs: 100, timeoutMs: 30000 }), { doubled: 10 })

  const failing = await db.fn('echo', { n: 1, fail: true }, { respondAsync: true })
  assert.ok(failing instanceof Job)
  await assert.rejects(
    failing.wait({ pollIntervalMs: 100, timeoutMs: 30000 }),
    (/** @type {any} */ err) => err instanceof ConflictError && err.code === 'out_of_stock',
  )

  // The same key replays the job it first made, whatever the second call prefers.
  const first = await db.fn('echo', { n: 6 }, { respondAsync: true, idempotencyKey: 'prefer-' + job.id })
  const again = /** @type {Job} */ (await db.fn('echo', { n: 6 }, { respondAsync: true, idempotencyKey: 'prefer-' + job.id }))
  assert.equal(/** @type {Job} */ (first).id, again.id)
})
