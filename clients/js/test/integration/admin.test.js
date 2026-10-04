import { test } from 'node:test'
import assert from 'node:assert/strict'

import { AuthenticationError, ConflictError, ForbiddenError, createClient } from '../../src/index.js'
import { apiKey, client, email, inviteApiKey, password, skip, url, user } from './env.js'

test('admin users and roles', { skip }, async () => {
  const admin = client({ apiKey }).admin
  const address = email('managed')
  const created = await admin.users.create({ email: address, password })
  assert.equal(created.email, address)
  assert.equal(created.disabled, false)
  assert.deepEqual(await admin.users.find(address.toUpperCase()), created)
  assert.equal(await admin.users.find(email('nobody')), null)
  await assert.rejects(admin.users.create({ email: address }), ConflictError)

  // The user signs in; disabling them ends the session.
  const u = client()
  await u.auth.login({ email: address, password })
  const verified = await admin.users.update(created.id, { emailVerified: true })
  assert.equal(verified.email_verified, true)
  await admin.users.update(created.id, { disabled: true })
  await assert.rejects(u.auth.me(), AuthenticationError)
  await assert.rejects(client().auth.login({ email: address, password }), AuthenticationError)
  await admin.users.update(created.id, { disabled: false })

  // A new password; the role grants deleting posts.
  await admin.users.setPassword(created.id, 'dev-p4ssw0rd!2')
  await u.auth.login({ email: address, password: 'dev-p4ssw0rd!2' })
  const post = await u.db('app').collection('posts').create({ title: 'to delete' })
  await assert.rejects(u.db('app').collection('posts').delete(post.id), ForbiddenError)
  assert.deepEqual((await admin.users.addRole(created.id, 'admin')).roles, ['admin'])
  await u.db('app').collection('posts').delete(post.id)
  assert.deepEqual((await admin.users.removeRole(created.id, 'admin')).roles, [])

  const page = await admin.users.list({ limit: 1 })
  assert.equal(page.items.length, 1)
  await admin.users.delete(created.id)
  assert.equal(await admin.users.find(address), null)

  // Sessions can't use the admin API.
  const ada = await user('notadmin')
  await assert.rejects(ada.c.request({ method: 'GET', path: ['_admin', 'users'] }), ForbiddenError)
})

test('owned: a preview of what erasing a user would do', { skip }, async () => {
  const admin = client({ apiKey }).admin
  const ada = await user('owner')
  const posts = ada.c.db('app').collection('posts')
  await posts.create({ title: 'one' })
  await posts.create({ title: 'two' })
  await client({ apiKey }).db('app').collection('private').create({ title: 'shared', members: [ada.email, email('other')] })

  const report = await admin.users.owned(ada.id)
  assert.deepEqual(report.user, { id: ada.id, status: 'active' })
  const byName = Object.fromEntries(report.collections.map((c) => [`${c.database}.${c.collection}`, c]))
  assert.equal(byName['app.posts'].action, 'delete')
  assert.equal(byName['app.posts'].owned, 2)
  assert.equal(byName['app.private'].action, null)
  assert.equal(byName['app.private'].pull?.members, 1)
  assert.ok(report.without_policy.includes('app.tags'), JSON.stringify(report.without_policy))
  assert.ok(!report.without_policy.includes('app.posts'))
  await assert.rejects(admin.users.owned('nope'), (/** @type {any} */ e) => e.status === 404)
})

test('erasing a user applies the collections\' policies', { skip }, async () => {
  const admin = client({ apiKey }).admin
  const ada = await user('erased')
  const posts = ada.c.db('app').collection('posts')
  await posts.create({ title: 'one' })
  await posts.create({ title: 'two' })
  const shared = await client({ apiKey }).db('app').collection('private').create({ title: 'shared', members: [ada.email, 'someone@example.com'] })

  const job = await admin.users.delete(ada.id)
  assert.ok(job.id)
  const erased = await admin.users.get(ada.id)
  assert.match(erased.email, /^erased-.+@erased\.invalid$/)
  assert.notEqual(erased.erased_at, null)
  assert.equal(erased.disabled, true)
  await assert.rejects(admin.users.update(ada.id, { disabled: false }), (/** @type {any} */ e) => e.status === 409 && e.code === 'user_erased')
  await assert.rejects(ada.c.auth.me(), AuthenticationError)

  // A worker applies the policies: her posts are deleted (action: delete), and she is out of the shared document.
  const keyed = client({ apiKey })
  /** @type {any} */ let left
  for (let i = 0; i < 100; i++) {
    left = await keyed.db('app').collection('posts').list({ where: { '_meta.owner': ada.id }, limit: 10 })
    if (left.items.length === 0) break
    await new Promise((r) => setTimeout(r, 200))
  }
  assert.equal(left.items.length, 0, 'her posts should be deleted')
  const doc = await keyed.db('app').collection('private').get(shared.id)
  assert.deepEqual(doc.members, ['someone@example.com'])
  assert.deepEqual((await admin.users.owned(ada.id)).user, { id: ada.id, status: 'erased' })
  await assert.rejects(admin.users.delete(ada.id), (/** @type {any} */ e) => e.status === 409 && e.code === 'already_erased')

  // The address is free for a new person.
  await client().auth.signup({ email: ada.email, password })
})

test('invitations', { skip }, async () => {
  const invite = (/** @type {Partial<import('../../src/index.js').ClientOptions>} */ o = {}) => createClient({ url, realm: 'invite', ...o })
  const admin = invite({ apiKey: inviteApiKey }).admin
  const address = email('invited')

  await assert.rejects(invite().auth.signup({ email: address, password }), (/** @type {any} */ e) => e instanceof ForbiddenError && /invitation/.test(e.message))

  const inv = await admin.invitations.create({ email: address, expiresIn: '1d' })
  assert.match(inv.token, /^bdi_/)
  assert.equal(inv.email, address)
  assert.ok((await admin.invitations.list()).some((x) => x.id === inv.id))

  await assert.rejects(invite().auth.signup({ email: email('eve'), password, invitation: inv.token }), ForbiddenError)
  const c = invite()
  await c.auth.signup({ email: address, password, invitation: inv.token })
  await c.db('app').collection('notes').create({ title: 'hello' })
  await assert.rejects(invite().auth.signup({ email: email('again'), password, invitation: inv.token }), ForbiddenError)

  const spare = await admin.invitations.create()
  assert.equal(spare.email, null)
  await admin.invitations.revoke(spare.id)
  await assert.rejects(invite().auth.signup({ email: email('late'), password, invitation: spare.token }), ForbiddenError)
})

test('API keys and network restrictions through the admin API', { skip }, async () => {
  const admin = client({ apiKey }).admin
  const name = 'svc-' + Math.random().toString(36).slice(2, 8)
  const created = await admin.apiKeys.create({ name, expiresIn: '1d' })
  assert.equal(created.role, 'data')
  assert.match(created.key, /^bdk_/)
  // A data key reaches data, not the admin API.
  const svc = client({ apiKey: created.key })
  await svc.db('app').collection('posts').list()
  await assert.rejects(svc.admin.apiKeys.list(), ForbiddenError)
  const listed = (await admin.apiKeys.list()).find((k) => k.name === name)
  assert.ok(listed && !('key' in listed))
  await assert.rejects(admin.apiKeys.create({ name }), ConflictError)
  await admin.apiKeys.revoke(name)
  await assert.rejects(svc.db('app').collection('posts').list(), AuthenticationError)

  // A scoped key reaches only what it was made for.
  const scoped = await admin.apiKeys.create({ name: name + '-ro', scopes: ['read:app/posts'], expiresIn: '1d' })
  assert.deepEqual(scoped.scopes, ['read:app/posts'])
  const ro = client({ apiKey: scoped.key })
  await ro.db('app').collection('posts').list()
  await assert.rejects(ro.db('app').collection('tags').list(), ForbiddenError)
  await assert.rejects(ro.db('app').collection('posts').create({ title: 'nope' }), ForbiddenError)
  assert.deepEqual((await admin.apiKeys.list()).find((k) => k.name === name + '-ro')?.scopes, ['read:app/posts'])
  await assert.rejects(admin.apiKeys.create({ name: name + '-bad', scopes: ['read:nope'] }), (/** @type {any} */ e) => e.status === 400)
  await admin.apiKeys.revoke(name + '-ro')

  // Both are in the audit trail, newest first, without the key.
  const trail = await admin.audit.list({ target: 'key:' + name })
  assert.deepEqual(trail.items.map((r) => r.action), ['apikey.revoke', 'apikey.create'])
  assert.ok(!JSON.stringify(trail).includes(created.key))

  // A user's login networks: from elsewhere, their session stops working.
  const address = email('netted')
  const u = await admin.users.create({ email: address, password })
  const session = client()
  await session.auth.login({ email: address, password })
  const netted = await admin.users.setNetworks(u.id, { loginNetworks: ['203.0.113.0/24'] })
  assert.deepEqual(netted.login_networks, ['203.0.113.0/24'])
  await assert.rejects(session.auth.me(), AuthenticationError)
  await admin.users.setNetworks(u.id, {})
  await admin.users.delete(u.id)
})
