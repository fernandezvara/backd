import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, RetryableError } from '../src/index.js'
import { mockFetch, errorBody } from './helpers.js'

/** @param {ReturnType<typeof mockFetch>} m */
const make = (m) => createClient({ url: 'http://api.test', realm: 'shelf', fetch: m.fetch, apiKey: 'bdk_test' })

const queued = { id: 'e1', status: 'queued', created_at: '2026-10-24T10:00:00.000Z' }
const ready = { id: 'e1', status: 'ready', created_at: '2026-10-24T10:00:00.000Z', expires_at: '2026-10-31T10:00:00.000Z', size: 12, counts: { files: 0 } }

test('requestExport sends the fresh proof the client holds', async () => {
  const m = mockFetch([{ body: { reauth_token: 'rt_1', expires_at: new Date(Date.now() + 60000).toISOString() } }, { status: 202, body: queued }])
  const c = make(m)
  await c.auth.reauth({ method: 'password', password: 'x' })
  assert.deepEqual(await c.account.requestExport(), queued)
  assert.equal(m.calls[1].method, 'POST')
  assert.equal(m.calls[1].url.pathname, '/v1/shelf/_auth/me/export')
  assert.equal(m.calls[1].headers['X-Backd-Reauth'], 'rt_1')
})

test('asking twice in a day is a retryable error that says how long to wait', async () => {
  const m = mockFetch([{ status: 429, body: errorBody('too_many_requests', 'ask again in 23h'), headers: { 'Retry-After': '82800' } }])
  await assert.rejects(make(m).account.requestExport(), (e) => e instanceof RetryableError && e.status === 429 && e.retryAfter === 82800000)
})

test('exports lists, export reads one and waitForExport follows it to the end', async () => {
  const running = { ...queued, status: 'running', step: 'Export: documents app/orders' }
  const m = mockFetch([{ body: { items: [ready] } }, { body: running }, { body: running }, { body: ready }])
  const c = make(m)
  assert.deepEqual(await c.account.exports(), [ready])
  assert.equal(m.calls[0].url.pathname, '/v1/shelf/_auth/me/exports')
  const seen = /** @type {string[]} */ ([])
  assert.equal((await c.account.export('e1')).step, 'Export: documents app/orders')
  const done = await c.account.waitForExport('e1', { pollIntervalMs: 1, onProgress: (e) => seen.push(e.status) })
  assert.equal(done.status, 'ready')
  assert.deepEqual(seen, ['running', 'ready'])
  assert.equal(m.calls[3].url.pathname, '/v1/shelf/_auth/me/exports/e1')
})

test('waitForExport stops on a failed or expired export, and on its timeout', async () => {
  let m = mockFetch([{ body: { ...queued, status: 'failed' } }])
  await assert.rejects(make(m).account.waitForExport('e1', { pollIntervalMs: 1 }), /export failed: ask for a new one/)
  m = mockFetch([{ body: { ...queued, status: 'expired' } }])
  await assert.rejects(make(m).account.waitForExport('e1', { pollIntervalMs: 1 }), /export expired/)
  m = mockFetch([{ body: queued }, { body: queued }, { body: queued }])
  await assert.rejects(make(m).account.waitForExport('e1', { pollIntervalMs: 5, timeoutMs: 8 }), /still queued/)
})

test('downloadExport gives the signed link, and its bytes from it', async () => {
  const zip = new Uint8Array([80, 75, 3, 4])
  const m = mockFetch([
    { body: { url: 'http://storage.test/pkg.zip?sig=1', expires_at: '2026-10-24T10:05:00.000Z' } },
    { bytes: String.fromCharCode(...zip) },
  ])
  const d = await make(m).account.downloadExport('e1')
  assert.equal(d.url, 'http://storage.test/pkg.zip?sig=1')
  assert.equal(m.calls[0].url.pathname, '/v1/shelf/_auth/me/exports/e1/download')
  assert.equal(m.calls[0].headers.Accept, 'application/json') // a page that fetches cannot follow a redirect to another origin
  assert.deepEqual([...(await d.bytes())], [...zip])
  assert.equal(m.calls[1].url.host, 'storage.test')
  assert.equal(m.calls[1].headers.Authorization, undefined) // the link is signed: no credentials go to the storage
})

test('a realm that proxies its files sends the zip itself', async () => {
  const m = mockFetch([{ bytes: 'PK-package', headers: { 'Content-Type': 'application/zip' } }])
  const d = await make(m).account.downloadExport('e1')
  assert.equal(d.url, undefined)
  assert.equal(new TextDecoder().decode(await d.bytes()), 'PK-package')
})

test('a package that is not ready, or is gone, is a typed error', async () => {
  const m = mockFetch([{ status: 409, body: errorBody('conflict', 'this export is queued') }, { status: 410, body: errorBody('gone', 'expired') }])
  const c = make(m)
  await assert.rejects(c.account.downloadExport('e1'), (/** @type {any} */ e) => e.status === 409)
  await assert.rejects(c.account.downloadExport('e1'), (/** @type {any} */ e) => e.status === 410)
})
