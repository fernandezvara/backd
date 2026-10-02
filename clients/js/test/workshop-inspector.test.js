import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createInspector, curlOf } from '../examples/workshop/lib/inspector.js'

/** A fetch that answers every call with the same response. */
function fakeFetch(/** @type {{ status?: number, body?: string, headers?: Record<string, string> }} */ { status = 200, body = '{"ok":true}', headers = {} } = {}) {
  return async () => new Response(body, { status, headers })
}

test('the inspector records calls, newest first, with their answer', async () => {
  let changes = 0
  const inspector = createInspector({ fetch: fakeFetch({ headers: { 'X-Request-ID': 'req-1' } }), onChange: () => changes++, now: () => 0 })
  inspector.step('First')
  await inspector.fetch('https://localhost:8443/v1/workshop/main/_func/order_total', {
    method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer secret-token' }, body: '{"order_id":"o1"}',
  })
  inspector.step('Second')
  await inspector.fetch('https://localhost:8443/v1/workshop/main/orders')

  assert.equal(inspector.entries.length, 2)
  const [newest, oldest] = inspector.entries
  assert.equal(newest.step, 'Second')
  assert.equal(newest.method, 'GET')
  assert.equal(newest.path, '/main/orders')
  assert.equal(oldest.step, 'First')
  assert.equal(oldest.method, 'POST')
  assert.equal(oldest.path, '/main/_func/order_total')
  assert.equal(oldest.status, 200)
  assert.equal(oldest.requestId, 'req-1')
  assert.equal(oldest.request, '{\n  "order_id": "o1"\n}')
  assert.equal(oldest.response, '{\n  "ok": true\n}')
  assert.ok(changes >= 4, 'the page is told when a call starts and ends')
})

test('the token is never recorded: curl shows $TOKEN', async () => {
  const inspector = createInspector({ fetch: fakeFetch() })
  await inspector.fetch('https://localhost:8443/v1/workshop/main/orders', { method: 'POST', headers: { Authorization: 'Bearer secret-token', 'Content-Type': 'application/json' }, body: '{"a":1}' })
  const [e] = inspector.entries
  assert.ok(!JSON.stringify(e).includes('secret-token'))
  assert.match(e.curl, /-H 'Authorization: Bearer \$TOKEN'/)
  assert.match(e.curl, /-d '\{"a":1\}'/)
})

test('curlOf quotes single quotes in the body', () => {
  const curl = curlOf({ method: 'POST', url: 'https://x/y', headers: {}, body: `{"n":"it's"}` })
  assert.equal(curl, `curl -s -X POST 'https://x/y' \\\n  -d '{"n":"it'\\''s"}'`)
})

test('dev-mode headers become the function log and its time', async () => {
  const logs = JSON.stringify([{ level: 'log', line: 'charging 1250' }])
  const inspector = createInspector({ fetch: fakeFetch({ headers: { 'X-Backd-Dev-Logs': logs, 'X-Backd-Dev-Duration-Ms': '42' } }) })
  await inspector.fetch('https://x/v1/workshop/main/_func/refund', { method: 'POST' })
  assert.deepEqual(inspector.entries[0].logs, [{ level: 'log', line: 'charging 1250' }])
  assert.equal(inspector.entries[0].executorMs, '42')
})

test('an expected refusal carries its note, once', async () => {
  const inspector = createInspector({ fetch: fakeFetch({ status: 401, body: '{"error":{"code":"invalid_credentials"}}' }) })
  inspector.expect(401, 'no account yet')
  await inspector.fetch('https://x/v1/workshop/_auth/login', { method: 'POST' })
  assert.equal(inspector.entries[0].note, 'no account yet')
  // It applied to one call: the next 401 is a real one.
  await inspector.fetch('https://x/v1/workshop/_auth/login', { method: 'POST' })
  assert.equal(inspector.entries[0].note, null)
  // A different status than expected carries no note.
  inspector.expect(409, 'exists')
  await inspector.fetch('https://x/v1/workshop/_auth/login', { method: 'POST' })
  assert.equal(inspector.entries[0].note, null)
})

test('a function that logged nothing gives an empty log, not null', async () => {
  const inspector = createInspector({ fetch: fakeFetch({ headers: { 'X-Backd-Dev-Logs': 'null' } }) })
  await inspector.fetch('https://x/v1/workshop/main/_func/order_total', { method: 'POST' })
  assert.deepEqual(inspector.entries[0].logs, [])
})

test('failures are recorded and still thrown', async () => {
  const inspector = createInspector({ fetch: async () => { throw new Error('network down') } })
  await assert.rejects(() => inspector.fetch('https://x/v1/workshop/main/orders'), /network down/)
  assert.equal(inspector.entries[0].error, 'network down')
  assert.equal(inspector.entries[0].status, null)

  const bad = createInspector({ fetch: fakeFetch({ status: 404, body: '{"error":{"code":"not_found"}}' }) })
  const res = await bad.fetch('https://x/v1/workshop/main/orders/o1')
  assert.equal(res.status, 404)
  assert.equal(await res.text(), '{"error":{"code":"not_found"}}', 'the caller can still read the body')
  assert.equal(bad.entries[0].status, 404)
})

test('long answers are cut, and clear empties the list', async () => {
  const inspector = createInspector({ fetch: fakeFetch({ body: 'x'.repeat(10000) }) })
  await inspector.fetch('https://x/v1/workshop/main/reports/r1')
  assert.ok(inspector.entries[0].response.length < 6200)
  assert.match(inspector.entries[0].response, /more characters/)
  inspector.clear()
  assert.equal(inspector.entries.length, 0)
})
