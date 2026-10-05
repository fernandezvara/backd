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
  await a.apiKeys.create({ name: 'plain', scopes: ['read:blog/posts', 'call:main/export'] })
  assert.deepEqual(m.calls[2].body, { name: 'plain', scopes: ['read:blog/posts', 'call:main/export'] })

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
    { body: adminUser }, { status: 204 }, { status: 202, body: { id: 'erase1', status: 'queued' } }, { body: adminUser }, { body: adminUser },
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

  assert.deepEqual(await a.users.delete('u1'), { id: 'erase1', status: 'queued' })
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

test('emailed invitations and an administrator\'s email change', async () => {
  const inv = { id: 'i1', email: 'e@example.com', created_by: 'key:svc', created_at: 'x', expires_at: 'y', sent: true }
  const m = mockFetch([{ status: 201, body: inv }, { body: { ...adminUser, email: 'ada.new@example.com', email_verified: true } }])
  const a = adminOf(m)
  const sent = await a.invitations.send({ email: 'e@example.com', expiresIn: '3d', redirectTo: 'https://app.example/welcome', locale: 'es' })
  assert.equal(sent.sent, true)
  assert.deepEqual(m.calls[0].body, { email: 'e@example.com', send: true, expires_in: '3d', redirect_to: 'https://app.example/welcome', locale: 'es' })
  const user = await a.users.changeEmail('u1', 'ada.new@example.com')
  assert.equal(user.email, 'ada.new@example.com')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_admin/users/u1/email')
  assert.deepEqual(m.calls[1].body, { email: 'ada.new@example.com' })
})

test('owned: what erasing a user would do', async () => {
  const report = {
    user: { id: 'u1', status: 'active' },
    collections: [{ database: 'main', collection: 'orders', action: 'anonymize', owned: 2, remove: ['phone'], replace: ['buyer'], pull: { members: 1 } }],
    without_policy: ['main.digests'],
  }
  const m = mockFetch([{ body: report }])
  assert.deepEqual(await adminOf(m).users.owned('u1'), report)
  assert.equal(m.calls[0].method, 'GET')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/users/u1/owned')
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

  await a.jobs.list({ function: 'app/nightly', status: 'done', origin: 'cron', scheduled: true, since: new Date('2026-09-29T00:00:00Z'), limit: 5 })
  const q = m.calls[1].url.searchParams
  assert.equal(q.get('function'), 'app/nightly')
  assert.equal(q.get('status'), 'done')
  assert.equal(q.get('origin'), 'cron')
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

test('secrets: list, set and delete', async () => {
  const info = { database: '', name: 'PAYMENT_WEBHOOK_SECRET', created_at: '2026-10-02T10:00:00.000Z', updated_at: '2026-10-02T10:00:00.000Z', updated_by: 'user:u1' }
  const m = mockFetch([{ body: { items: [info] } }, { status: 204 }, { status: 204 }, { status: 204 }])
  const a = adminOf(m)
  assert.deepEqual(await a.secrets.list(), [info])
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/secrets')

  await a.secrets.set('PAYMENT_WEBHOOK_SECRET', 'whsec_test')
  assert.equal(m.calls[1].method, 'PUT')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_admin/secrets/PAYMENT_WEBHOOK_SECRET')
  assert.deepEqual(m.calls[1].body, { value: 'whsec_test' })

  await a.secrets.set('STRIPE_KEY', 'sk', { database: 'main' })
  assert.deepEqual(m.calls[2].body, { value: 'sk', database: 'main' })

  const m2 = mockFetch([{ status: 204 }, { status: 204 }])
  const b = adminOf(m2)
  await b.secrets.delete('STRIPE_KEY')
  assert.equal(m2.calls[0].method, 'DELETE')
  assert.equal(m2.calls[0].url.search, '')
  await b.secrets.delete('STRIPE_KEY', { database: 'main' })
  assert.equal(m2.calls[1].url.searchParams.get('database'), 'main')
})

test('invocations listing', async () => {
  const rec = { id: 'i1', at: '2026-10-02T10:00:00.000Z', function: 'main/refund_receipt', actor: 'user:u1', mode: 'async', status: 'ok', code: null,
    duration_ms: 12, request_id: 'r1', job_id: 'j1', parent_id: 'i0', origin: 'function', logs: [] }
  const page = { items: [rec], limit: 50, skip: 0, has_more: false }
  const m = mockFetch([{ body: page }, { body: page }])
  const a = adminOf(m)
  assert.deepEqual(await a.invocations.list(), page)
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/invocations')
  await a.invocations.list({ function: 'main/refund', requestId: 'r1', since: new Date('2026-10-01T00:00:00Z'), limit: 5 })
  const q = m.calls[1].url.searchParams
  assert.equal(q.get('function'), 'main/refund')
  assert.equal(q.get('request_id'), 'r1')
  assert.equal(q.get('since'), '2026-10-01T00:00:00.000Z')
  assert.equal(q.get('limit'), '5')
})

test('users.list passes after and returns next_cursor', async () => {
  const m = mockFetch([{ body: { items: [], limit: 2, skip: 0, has_more: true, next_cursor: 'b@example.com' } }])
  const page = await adminOf(m).users.list({ limit: 2, after: 'a@example.com' })
  assert.equal(m.calls[0].url.searchParams.get('after'), 'a@example.com')
  assert.equal(m.calls[0].url.searchParams.get('skip'), null)
  assert.equal(page.next_cursor, 'b@example.com')
})

test('whoami, config, user search and sessions', async () => {
  const access = { level: 'read', write: [], read: ['config', 'functions'], read_access: { users: false, data: false }, user: { id: 'u1', email: 'ada@example.com', roles: ['support'] } }
  const config = { realm: 'acme', fingerprint: 'abc', file: 'acme/realm.yaml', settings: {}, databases: {}, templates: {}, warnings: [] }
  const session = { id: 's1', created_at: '2026-10-01T10:00:00.000Z', last_used_at: '2026-10-01T10:00:00.000Z', expires_at: '2026-10-08T10:00:00.000Z' }
  const m = mockFetch([{ body: access }, { body: config }, { body: { items: [adminUser], limit: 20, skip: 0, has_more: false } }, { body: { items: [session] } }, { status: 204 }])
  const a = adminOf(m)
  assert.deepEqual(await a.whoami(), access)
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/whoami')
  assert.deepEqual(await a.config(), config)
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_admin/config')
  await a.users.list({ q: 'ada' })
  assert.equal(m.calls[2].url.searchParams.get('q'), 'ada')
  assert.deepEqual(await a.users.sessions('u1'), [session])
  assert.equal(m.calls[3].url.pathname, '/v1/acme/_admin/users/u1/sessions')
  await a.users.revokeSession('u1', 's1')
  assert.equal(m.calls[4].method, 'DELETE')
  assert.equal(m.calls[4].url.pathname, '/v1/acme/_admin/users/u1/sessions/s1')
})
