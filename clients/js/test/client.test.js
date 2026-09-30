import { test } from 'node:test'
import assert from 'node:assert/strict'

import {
  createClient,
  BackdError,
  ValidationError,
  AuthenticationError,
  ForbiddenError,
  NotFoundError,
  ConflictError,
  VersionMismatchError,
  RetryableError,
  NetworkError,
} from '../src/index.js'
import { mockFetch, errorBody } from './helpers.js'

test('createClient requires url and realm', () => {
  // @ts-expect-error missing options on purpose
  assert.throws(() => createClient({ realm: 'r' }), /`url` is required/)
  // @ts-expect-error missing options on purpose
  assert.throws(() => createClient({ url: 'http://x' }), /`realm` is required/)
})

test('requests go to /v1/{realm}/… with encoded segments and query', async () => {
  const m = mockFetch([{ body: { ok: true } }])
  const c = createClient({ url: 'http://api.test/', realm: 'my realm', fetch: m.fetch, headers: { 'X-App': '1' } })
  const res = await c.request({ method: 'GET', path: ['db', 'c/d'], query: { a: 1, b: undefined, c: true } })
  assert.equal(m.calls[0].url.pathname, '/v1/my%20realm/db/c%2Fd')
  assert.equal(m.calls[0].url.search, '?a=1&c=true')
  assert.equal(m.calls[0].headers['X-App'], '1')
  assert.equal(m.calls[0].headers.Accept, 'application/json')
  assert.equal(m.calls[0].headers.Authorization, undefined)
  assert.deepEqual(res.data, { ok: true })
})

test('bodies are JSON with a content type; empty answers give undefined data', async () => {
  const m = mockFetch([{ status: 204 }, { status: 204 }])
  const c = createClient({ url: 'http://api.test', realm: 'r', fetch: m.fetch })
  const res = await c.request({ method: 'POST', path: ['x'], body: { a: 1 } })
  assert.equal(res.data, undefined)
  assert.equal(m.calls[0].headers['Content-Type'], 'application/json')
  assert.deepEqual(m.calls[0].body, { a: 1 })
  await c.request({ method: 'PATCH', path: ['x'], body: {}, contentType: 'application/merge-patch+json' })
  assert.equal(m.calls[1].headers['Content-Type'], 'application/merge-patch+json')
})

test('an API key is sent on every request and wins over a stored session', async () => {
  const m = mockFetch([{ body: {} }])
  const c = createClient({ url: 'http://api.test', realm: 'r', apiKey: 'bdk_key', fetch: m.fetch })
  await c.storage.set('bds_session')
  await c.request({ method: 'GET', path: ['x'] })
  assert.equal(m.calls[0].headers.Authorization, 'Bearer bdk_key')
})

test('API keys are refused in browsers unless explicitly allowed', () => {
  const g = /** @type {any} */ (globalThis)
  g.window = { document: {} }
  try {
    assert.throws(() => createClient({ url: 'http://api.test', realm: 'r', apiKey: 'bdk_key' }), /must not be used in browsers/)
    assert.doesNotThrow(() => createClient({ url: 'http://api.test', realm: 'r', apiKey: 'bdk_key', dangerouslyAllowBrowser: true }))
    assert.doesNotThrow(() => createClient({ url: 'http://api.test', realm: 'r' }))
  } finally {
    delete g.window
  }
  assert.doesNotThrow(() => createClient({ url: 'http://api.test', realm: 'r', apiKey: 'bdk_key' }))
})

test('error answers become typed errors', async () => {
  const cases = [
    [400, 'validation_error', ValidationError],
    [401, 'unauthenticated', AuthenticationError],
    [403, 'forbidden', ForbiddenError],
    [404, 'not_found', NotFoundError],
    [409, 'email_taken', ConflictError],
    [412, 'version_mismatch', VersionMismatchError],
    [429, 'too_many_requests', RetryableError],
    [503, 'unavailable', RetryableError],
    [500, 'internal_error', BackdError],
  ]
  for (const [status, code, Class] of cases) {
    const m = mockFetch([{ status: /** @type {number} */ (status), body: { error: { code, message: 'nope', request_id: 'rid', details: [{ path: 'f', reason: 'bad' }] } }, headers: { 'Retry-After': '7' } }])
    const c = createClient({ url: 'http://api.test', realm: 'r', fetch: m.fetch })
    await assert.rejects(c.request({ method: 'POST', path: ['x'] }), (/** @type {any} */ err) => {
      assert.ok(err instanceof /** @type {any} */ (Class), `${status} → ${err.constructor.name}`)
      assert.ok(err instanceof BackdError)
      assert.equal(err.status, status)
      assert.equal(err.code, code)
      assert.equal(err.message, 'nope')
      assert.equal(err.requestId, 'rid')
      assert.deepEqual(err.details, [{ path: 'f', reason: 'bad' }])
      if (err instanceof RetryableError) assert.equal(err.retryAfter, 7000)
      return true
    })
  }
})

test('non-JSON error answers still become errors', async () => {
  const fetch = async () => new Response('<html>bad gateway</html>', { status: 502, statusText: 'Bad Gateway', headers: { 'X-Request-ID': 'rid' } })
  const c = createClient({ url: 'http://api.test', realm: 'r', fetch })
  await assert.rejects(c.request({ method: 'GET', path: ['x'] }), (/** @type {any} */ err) => {
    assert.equal(err.status, 502)
    assert.equal(err.code, 'http_502')
    assert.equal(err.requestId, 'rid')
    return true
  })
})

test('network failures and aborts become NetworkError', async () => {
  const m = mockFetch([new TypeError('fetch failed')])
  const c = createClient({ url: 'http://api.test', realm: 'r', fetch: m.fetch })
  await assert.rejects(c.request({ method: 'GET', path: ['x'] }), (/** @type {any} */ err) => err instanceof NetworkError && err.code === 'network_error' && err.status === 0)

  const ac = new AbortController()
  ac.abort()
  const aborting = /** @type {typeof fetch} */ (async (_url, init) => {
    init?.signal?.throwIfAborted()
    return new Response('{}')
  })
  const c2 = createClient({ url: 'http://api.test', realm: 'r', fetch: aborting })
  await assert.rejects(c2.request({ method: 'GET', path: ['x'], signal: ac.signal }), (/** @type {any} */ err) => err instanceof NetworkError && err.code === 'aborted')
})

test('retries are off by default', async () => {
  const m = mockFetch([{ status: 503, body: errorBody('unavailable'), headers: { 'Retry-After': '0' } }])
  const c = createClient({ url: 'http://api.test', realm: 'r', fetch: m.fetch })
  await assert.rejects(c.request({ method: 'GET', path: ['x'] }), RetryableError)
  assert.equal(m.calls.length, 1)
})

test('retries honor Retry-After, only for safe requests', async () => {
  const busy = { status: 503, body: errorBody('unavailable'), headers: { 'Retry-After': '0' } }
  const m = mockFetch([busy, { status: 429, body: errorBody('too_many_requests'), headers: { 'Retry-After': '0' } }, { body: { ok: 1 } }])
  const c = createClient({ url: 'http://api.test', realm: 'r', fetch: m.fetch, retry: { attempts: 2 } })
  assert.deepEqual((await c.request({ method: 'GET', path: ['x'] })).data, { ok: 1 })
  assert.equal(m.calls.length, 3)

  // Writes without If-Match aren't retried: they might have taken effect.
  const w = mockFetch([busy])
  const cw = createClient({ url: 'http://api.test', realm: 'r', fetch: w.fetch, retry: { attempts: 2 } })
  await assert.rejects(cw.request({ method: 'POST', path: ['x'], body: {} }), RetryableError)
  assert.equal(w.calls.length, 1)

  // With If-Match they are.
  const wm = mockFetch([busy, { body: { ok: 2 } }])
  const cm = createClient({ url: 'http://api.test', realm: 'r', fetch: wm.fetch, retry: { attempts: 1 } })
  await cm.request({ method: 'PUT', path: ['x'], body: {}, headers: { 'If-Match': '"1"' } })
  assert.equal(wm.calls.length, 2)

  // Other errors are never retried; attempts run out.
  const n = mockFetch([{ status: 404, body: errorBody('not_found') }])
  const cn = createClient({ url: 'http://api.test', realm: 'r', fetch: n.fetch, retry: { attempts: 3 } })
  await assert.rejects(cn.request({ method: 'GET', path: ['x'] }), NotFoundError)
  assert.equal(n.calls.length, 1)
  const e = mockFetch([busy, busy])
  const ce = createClient({ url: 'http://api.test', realm: 'r', fetch: e.fetch })
  await assert.rejects(ce.request({ method: 'GET', path: ['x'], retry: { attempts: 1 } }), RetryableError)
  assert.equal(e.calls.length, 2)
})

test('waits are capped by maxDelayMs', async () => {
  const m = mockFetch([{ status: 503, body: errorBody('unavailable'), headers: { 'Retry-After': '3600' } }, { body: {} }])
  const c = createClient({ url: 'http://api.test', realm: 'r', fetch: m.fetch, retry: { attempts: 1, maxDelayMs: 10 } })
  const start = Date.now()
  await c.request({ method: 'GET', path: ['x'] })
  assert.ok(Date.now() - start < 1000)
})
