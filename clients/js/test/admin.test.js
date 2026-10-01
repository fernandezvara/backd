import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, NotFoundError } from '../src/index.js'
import { mockFetch, errorBody } from './helpers.js'

const adminUser = {
  id: 'u1', email: 'ada@example.com', email_verified: false, roles: [], disabled: false, admin_networks: [], login_networks: [],
  created_at: '2026-09-26T12:00:00.000Z', updated_at: '2026-09-26T12:00:00.000Z',
}

/** @param {ReturnType<typeof mockFetch>} m */
const adminOf = (m) => createClient({ url: 'http://api.test', realm: 'acme', apiKey: 'bdk_k', fetch: m.fetch }).admin

test('admin needs an API key or a session', async () => {
  const c = createClient({ url: 'http://api.test', realm: 'acme', fetch: mockFetch([]).fetch })
  await assert.rejects(c.admin.users.list(), /needs a client created with an `apiKey`/)
  await assert.rejects(c.admin.invitations.create(), /apiKey/)

  // A signed-in user's session is sent; the server decides (admin roles).
  const m = mockFetch([{ body: { items: [] } }])
  const signedIn = createClient({ url: 'http://api.test', realm: 'acme', fetch: m.fetch, storage: { get: () => 'bds_admin', set() {}, remove() {} } })
  assert.deepEqual(await signedIn.admin.apiKeys.list(), [])
  assert.equal(m.calls[0].headers.Authorization, 'Bearer bds_admin')
})

test('API keys and networks', async () => {
  const info = { name: 'billing', role: 'data', prefix: 'bdk_abcdef', networks: [], created_at: '2026-09-26T12:00:00.000Z', last_used_at: null, expires_at: null }
  const m = mockFetch([
    { body: { items: [info] } }, { status: 201, body: { ...info, key: 'bdk_secret' } }, { status: 201, body: { ...info, key: 'bdk_2' } },
    { status: 204 }, { body: adminUser },
  ])
  const a = adminOf(m)
  assert.deepEqual(await a.apiKeys.list(), [info])
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/apikeys')

  const created = await a.apiKeys.create({ name: 'billing', role: 'admin', expiresIn: '90d', networks: ['10.0.0.0/8'] })
  assert.equal(created.key, 'bdk_secret')
  assert.equal(m.calls[1].method, 'POST')
  assert.deepEqual(m.calls[1].body, { name: 'billing', role: 'admin', expires_in: '90d', networks: ['10.0.0.0/8'] })
  await a.apiKeys.create({ name: 'plain' })
  assert.deepEqual(m.calls[2].body, { name: 'plain' })

  await a.apiKeys.revoke('billing')
  assert.equal(m.calls[3].method, 'DELETE')
  assert.equal(m.calls[3].url.pathname, '/v1/acme/_admin/apikeys/billing')

  await a.users.setNetworks('u1', { loginNetworks: ['192.0.2.0/24'] })
  assert.equal(m.calls[4].method, 'PUT')
  assert.equal(m.calls[4].url.pathname, '/v1/acme/_admin/users/u1/networks')
  assert.deepEqual(m.calls[4].body, { admin_networks: [], login_networks: ['192.0.2.0/24'] })
})

test('audit trail', async () => {
  const page = { items: [{ id: 'a1', at: '2026-09-27T12:00:00.000Z', action: 'role.add', actor: 'user:u0', target: 'user:u1', details: { role: 'editor' }, request_id: 'r1', client_ip: '192.0.2.1' }], limit: 20, skip: 0, has_more: false }
  const m = mockFetch([{ body: page }, { body: page }])
  const a = adminOf(m)
  assert.deepEqual(await a.audit.list(), page)
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/audit')
  assert.equal(m.calls[0].url.search, '')
  await a.audit.list({ target: 'user:u1', since: new Date('2026-09-27T00:00:00Z'), limit: 5 })
  assert.equal(m.calls[1].url.searchParams.get('target'), 'user:u1')
  assert.equal(m.calls[1].url.searchParams.get('since'), '2026-09-27T00:00:00.000Z')
  assert.equal(m.calls[1].url.searchParams.get('limit'), '5')
})

test('users', async () => {
  const page = { items: [adminUser], limit: 5, skip: 0, has_more: false }
  const m = mockFetch([
    { body: page }, { body: page }, { body: { items: [], limit: 20, skip: 0, has_more: false } },
    { body: adminUser }, { status: 201, body: adminUser }, { status: 201, body: adminUser },
    { body: adminUser }, { status: 204 }, { status: 204 }, { body: adminUser }, { body: adminUser },
    { status: 404, body: errorBody('not_found') },
  ])
  const a = adminOf(m)
  assert.deepEqual(await a.users.list({ limit: 5 }), page)
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/users')
  assert.equal(m.calls[0].url.searchParams.get('limit'), '5')
  assert.equal(m.calls[0].headers.Authorization, 'Bearer bdk_k')

  assert.deepEqual(await a.users.find('Ada@example.com'), adminUser)
  assert.equal(m.calls[1].url.searchParams.get('email'), 'Ada@example.com')
  assert.equal(await a.users.find('nobody@example.com'), null)

  await a.users.get('u1')
  assert.equal(m.calls[3].url.pathname, '/v1/acme/_admin/users/u1')

  await a.users.create({ email: 'b@example.com', password: 'dev-p4ssw0rd!' })
  assert.deepEqual(m.calls[4].body, { email: 'b@example.com', password: 'dev-p4ssw0rd!' })
  await a.users.create({ email: 'c@example.com' })
  assert.deepEqual(m.calls[5].body, { email: 'c@example.com' })

  await a.users.update('u1', { emailVerified: true, disabled: false })
  assert.equal(m.calls[6].method, 'PATCH')
  assert.equal(m.calls[6].headers['Content-Type'], 'application/json')
  assert.deepEqual(m.calls[6].body, { email_verified: true, disabled: false })

  await a.users.setPassword('u1', 'dev-p4ssw0rd!2')
  assert.equal(m.calls[7].url.pathname, '/v1/acme/_admin/users/u1/password')
  assert.deepEqual(m.calls[7].body, { password: 'dev-p4ssw0rd!2' })

  await a.users.delete('u1')
  assert.equal(m.calls[8].method, 'DELETE')

  await a.users.addRole('u1', 'admin')
  assert.equal(m.calls[9].method, 'PUT')
  assert.equal(m.calls[9].url.pathname, '/v1/acme/_admin/users/u1/roles/admin')
  await a.users.removeRole('u1', 'admin')
  assert.equal(m.calls[10].method, 'DELETE')

  await assert.rejects(a.users.get('nope'), NotFoundError)
})

test('invitations', async () => {
  const inv = { id: 'i1', email: null, created_by: 'key:svc', created_at: 'x', expires_at: 'y' }
  const m = mockFetch([{ status: 201, body: { ...inv, token: 'bdi_t' } }, { status: 201, body: { ...inv, token: 'bdi_u' } }, { body: { items: [inv] } }, { status: 204 }])
  const a = adminOf(m)
  assert.equal((await a.invitations.create({ email: 'e@example.com', expiresIn: '3d' })).token, 'bdi_t')
  assert.deepEqual(m.calls[0].body, { email: 'e@example.com', expires_in: '3d' })
  await a.invitations.create()
  assert.deepEqual(m.calls[1].body, {})
  assert.deepEqual(await a.invitations.list(), [inv])
  await a.invitations.revoke('i1')
  assert.equal(m.calls[3].url.pathname, '/v1/acme/_admin/invitations/i1')
})

test('jobs listing', async () => {
  const job = { id: 'cron_app_nightly_202609290300', function: 'app/nightly', status: 'done', scheduled: true, attempts: 1,
    created_at: '2026-09-29T03:00:05.000Z', completed_at: '2026-09-29T03:00:09.000Z', result: { status: 'ok', code: null, duration_ms: 3800 } }
  const page = { items: [job], limit: 50, skip: 0, has_more: false }
  const m = mockFetch([{ body: page }, { body: page }])
  const a = adminOf(m)
  assert.deepEqual(await a.jobs.list(), page)
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/jobs')
  assert.equal(m.calls[0].method, 'GET')

  await a.jobs.list({ function: 'app/nightly', status: 'done', scheduled: true, since: new Date('2026-09-29T00:00:00Z'), limit: 5 })
  const q = m.calls[1].url.searchParams
  assert.equal(q.get('function'), 'app/nightly')
  assert.equal(q.get('status'), 'done')
  assert.equal(q.get('scheduled'), 'true')
  assert.equal(q.get('since'), '2026-09-29T00:00:00.000Z')
  assert.equal(q.get('limit'), '5')
})

test('invokeFunction runs a function by hand', async () => {
  const jobData = { id: 'j1', function: 'app/cleanup', status: 'queued', created_at: '2026-10-01T00:00:00.000Z', result: null }
  const m = mockFetch([{ body: { sent: true } }, { status: 202, body: jobData }])
  const a = adminOf(m)
  assert.deepEqual(await a.invokeFunction('app/send', { input: { n: 1 }, as: 'ana@example.com', idempotencyKey: 'k1' }), { sent: true })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/functions/app/send/invoke')
  assert.equal(m.calls[0].method, 'POST')
  assert.deepEqual(m.calls[0].body, { input: { n: 1 }, as: 'ana@example.com' })
  assert.equal(m.calls[0].headers['Idempotency-Key'], 'k1')

  const job = /** @type {import('../src/functions.js').Job} */ (await a.invokeFunction('app/cleanup'))
  assert.deepEqual(m.calls[1].body, { input: null })
  assert.equal(job.id, 'j1')

  await assert.rejects(() => a.invokeFunction('cleanup'), TypeError)
})
