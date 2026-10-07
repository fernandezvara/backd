import { test } from 'node:test'
import assert from 'node:assert/strict'

import { ConflictError, createClient, NotFoundError } from '../src/index.js'
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

test('data goes through the admin data route', async () => {
  const doc = { id: 'p1', title: 'Hello', _meta: { created_at: '2026-10-01T00:00:00.000Z', updated_at: '2026-10-01T00:00:00.000Z', version: 1 } }
  const m = mockFetch([
    { body: { items: [doc], limit: 20, skip: 0, has_more: false } }, { body: doc }, { status: 201, body: doc }, { body: { ...doc, _meta: { ...doc._meta, version: 2 } } },
    { status: 204 }, { body: doc },
  ])
  const posts = adminOf(m).data('blog', 'posts')
  await posts.list({ where: { title: 'Hello' }, deleted: 'include' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/data/blog/posts')
  assert.equal(m.calls[0].url.searchParams.get('deleted'), 'include')
  await posts.get('p1')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_admin/data/blog/posts/p1')
  await posts.create({ title: 'Hello' })
  assert.equal(m.calls[2].method, 'POST')
  assert.equal(m.calls[2].url.pathname, '/v1/acme/_admin/data/blog/posts')
  await posts.replace('p1', { title: 'Hi' }, { ifMatch: 1 })
  assert.equal(m.calls[3].method, 'PUT')
  assert.equal(m.calls[3].headers['If-Match'], '"1"')
  await posts.delete('p1', { purge: true })
  assert.equal(m.calls[4].url.searchParams.get('purge'), 'true')
  await posts.restore('p1')
  assert.equal(m.calls[5].url.pathname, '/v1/acme/_admin/data/blog/posts/p1/restore')
})

test('jobs can be cancelled and re-run', async () => {
  const job = { id: 'j1', function: 'app/report', status: 'done', scheduled: false, origin: 'http', email_kind: null, rerun_of: null, attempts: 1, next_attempt_at: null, created_at: '2026-10-05T10:00:00.000Z', completed_at: '2026-10-05T10:00:01.000Z', result: { status: 'cancelled', code: null, duration_ms: 0 } }
  const again = { ...job, id: 'j2', status: 'queued', origin: 'admin', rerun_of: 'j1', completed_at: null, result: null }
  const m = mockFetch([{ body: job }, { status: 202, body: again }, { status: 409, body: errorBody('conflict', 'the job has already finished') }])
  const a = adminOf(m)
  assert.equal((await a.jobs.cancel('j1')).result?.status, 'cancelled')
  assert.equal(m.calls[0].method, 'POST')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/jobs/j1/cancel')
  const second = await a.jobs.rerun('j1')
  assert.equal(second.rerun_of, 'j1')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_admin/jobs/j1/rerun')
  await assert.rejects(a.jobs.cancel('j1'), ConflictError)
})

test('schedules can be listed, paused and resumed', async () => {
  const nightly = { function: 'app/nightly', schedule: '0 3 * * *', timezone: 'UTC', overlap: 'allow', paused: false, changed_at: null, changed_by: null }
  const m = mockFetch([{ body: { items: [nightly] } }, { body: { ...nightly, paused: true, changed_at: '2026-10-05T10:00:00Z', changed_by: 'key:ops' } }, { body: nightly }, { status: 409, body: errorBody('conflict', 'the function has no schedule') }])
  const a = adminOf(m)
  assert.deepEqual((await a.schedules.list()).map((s) => s.function), ['app/nightly'])
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/schedules')
  const paused = await a.schedules.pause('app/nightly')
  assert.equal(paused.paused, true)
  assert.equal(m.calls[1].method, 'POST')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_admin/functions/app/nightly/pause')
  assert.equal((await a.schedules.resume('app/nightly')).paused, false)
  assert.equal(m.calls[2].url.pathname, '/v1/acme/_admin/functions/app/nightly/resume')
  await assert.rejects(a.schedules.pause('app/plain'), ConflictError)
  await assert.rejects(a.schedules.pause('nightly'), TypeError)
})

test('jobs.get returns one job with its steps', async () => {
  const step = { n: 1, name: 'load', status: 'running', started_at: '2026-10-06T10:00:00.000Z', ended_at: null, duration_ms: null, current: 4, total: 10, message: 'rows', updated_at: '2026-10-06T10:00:01.000Z' }
  const job = { id: 'j1', function: 'app/report', status: 'running', scheduled: false, origin: 'http', email_kind: null, rerun_of: null, attempts: 1, next_attempt_at: null, created_at: '2026-10-06T10:00:00.000Z', completed_at: null, result: null, progress: { step: 1, name: 'load', status: 'running', current: 4, total: 10, message: 'rows', updated_at: step.updated_at }, steps: [step], steps_omitted: 0 }
  const m = mockFetch([{ body: job }, { status: 404, body: errorBody('not_found', 'job not found') }])
  const a = adminOf(m)
  const got = await a.jobs.get('j1')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/jobs/j1')
  assert.equal(got.steps?.[0].current, 4)
  assert.equal(got.progress?.name, 'load')
  await assert.rejects(a.jobs.get('nope'), NotFoundError)
})

test('dataChecks start, list, get and wait', async () => {
  const summary = { database: 'app', collection: 'notes', job_id: 'j1', started_at: '2026-10-07T09:00:00.000Z', finished_at: '2026-10-07T09:01:00.000Z', scanned: 1000, invalid: 1, complete: true, stopped_by: null, limit: 100, schema_hash: 'abc' }
  const report = { ...summary, documents: [{ id: 'n1', deleted: false, problems: [{ path: 'title', reason: 'is required' }], more_problems: 0 }] }
  /** @type {(status: string, extra?: Record<string, unknown>) => any} */
  const job = (status, extra = {}) => ({ id: 'j1', function: 'app/notes', status, scheduled: false, origin: 'backd:schema.check', email_kind: null, rerun_of: null, attempts: 1, next_attempt_at: null, created_at: '2026-10-07T09:00:00.000Z', completed_at: null, result: null, progress: null, ...extra })
  const started = { id: 'j1', status: 'queued', scope: 'app/notes', collections: ['app/notes'], limit: 100, estimated_documents: 1000, created_at: '2026-10-07T09:00:00.000Z' }
  const m = mockFetch([
    { status: 202, body: started },
    { status: 409, body: { error: { code: 'check_running', message: 'a schema check is already queued or running', details: [{ path: 'job_id', reason: 'j1' }], request_id: 'r' } } },
    { body: { running: job('running'), items: [{ database: 'app', collection: 'notes', report: summary }, { database: 'app', collection: 'labels', report: null }] } },
    { body: job('running', { progress: { step: 1, name: 'app/notes', status: 'running', current: 45, total: 100, message: 'scanned 450', updated_at: '2026-10-07T09:00:30.000Z' } }) },
    { body: job('done', { completed_at: '2026-10-07T09:01:00.000Z', result: { status: 'ok', code: null, duration_ms: 1 } }) },
    { body: report },
    { status: 404, body: errorBody('not_found', 'this collection has no schema check report yet') },
  ])
  const a = adminOf(m)
  const got = await a.dataChecks.start({ database: 'app', collection: 'notes' })
  assert.equal(got.estimated_documents, 1000)
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/data-checks')
  assert.deepEqual(m.calls[0].body, { database: 'app', collection: 'notes' })
  await assert.rejects(a.dataChecks.start(), (/** @type {any} */ e) => e instanceof ConflictError && e.details[0].reason === 'j1')
  const list = await a.dataChecks.list()
  assert.equal(list.running?.id, 'j1')
  assert.equal(list.items[1].report, null)
  /** @type {number[]} */
  const seen = []
  const reports = await a.dataChecks.wait(got, { pollIntervalMs: 0, onProgress: (j) => seen.push(j.progress?.current ?? -1) })
  assert.deepEqual(seen, [45, -1])
  assert.equal(reports[0].documents[0].problems[0].reason, 'is required')
  assert.equal(m.calls[5].url.pathname, '/v1/acme/_admin/data-checks/app/notes')
  await assert.rejects(a.dataChecks.get('app', 'labels'), NotFoundError)
})

test('storage.check posts to the storage check route', async () => {
  const report = { ok: true, provider: 'minio', endpoint: 'http://minio:9000', public_endpoint: null, bucket: 'files', prefix: 'dev', steps: [{ name: 'bucket', level: 'ok', detail: 'files exists' }], checksum_sha256: 'verified', encryption: 'unknown', cors: [], link_host: 'minio:9000' }
  const m = mockFetch([{ body: report }, { status: 404, body: errorBody('not_found', 'this realm has no storage configured') }])
  const a = adminOf(m)
  const got = await a.storage.check()
  assert.equal(m.calls[0].method, 'POST')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/storage/check')
  assert.equal(got.checksum_sha256, 'verified')
  await assert.rejects(a.storage.check(), NotFoundError)
})

test('storage.status and storage.reconcile use the storage routes', async () => {
  const m = mockFetch([{ body: { configured: true, usage: { bytes: 5, files: 1, users: [] } } }, { body: { delete: true, orphans: [], deleted: 0 } }])
  const a = createClient({ url: 'https://api.test', realm: 'acme', apiKey: 'bdk_x', fetch: m.fetch }).admin
  assert.equal((await a.storage.status()).usage?.files, 1)
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/storage')
  assert.equal(m.calls[0].method, 'GET')
  await a.storage.reconcile({ delete: true })
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_admin/storage/reconcile')
  assert.deepEqual(m.calls[1].body, { delete: true })
})

test('checkConfig posts the draft files and answers what was found', async () => {
  const m = mockFetch([{ body: { ok: false, realm: 'acme', checked: 12, changed: ['main/a/rules.yaml'], problems: [{ file: 'main/a/rules.yaml', message: 'main/a/rules.yaml: unknown field' }] } }])
  const a = createClient({ url: 'https://api.test', realm: 'acme', apiKey: 'bdk_x', fetch: m.fetch }).admin
  const out = await a.checkConfig({ 'main/a/rules.yaml': 'read: x\n', 'main/b/schema.json': null })
  assert.equal(out.ok, false)
  assert.equal(out.problems[0].file, 'main/a/rules.yaml')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_admin/config/check')
  assert.deepEqual(m.calls[0].body, { files: { 'main/a/rules.yaml': 'read: x\n', 'main/b/schema.json': null } })
})
