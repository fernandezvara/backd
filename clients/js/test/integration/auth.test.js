import { test } from 'node:test'
import assert from 'node:assert/strict'

import { AuthenticationError, ConflictError, RetryableError, ValidationError } from '../../src/index.js'
import { client, email, password, skip, user } from './env.js'

test('sign-up, me, sessions, logout', { skip }, async () => {
  const c = client()
  /** @type {string[]} */
  const events = []
  c.auth.onAuthChange((e) => events.push(e))
  const address = email('ada')
  const s = await c.auth.signup({ email: address.toUpperCase(), password })
  assert.match(String(s.token), /^bds_/)
  assert.equal(s.user.email, address)
  assert.deepEqual(await c.auth.me(), s.user)

  await assert.rejects(client().auth.signup({ email: address, password }), ConflictError)
  await assert.rejects(client().auth.signup({ email: 'not-an-email', password }), (/** @type {any} */ e) => e instanceof ValidationError && e.details[0].path === 'email')

  const other = client()
  await other.auth.login({ email: address, password })
  const list = await c.auth.sessions()
  assert.equal(list.length, 2)
  assert.equal(list.filter((x) => x.current).length, 1)

  await c.auth.logout()
  assert.equal(await c.auth.token(), null)
  await assert.rejects(c.auth.me(), AuthenticationError)
  assert.deepEqual(events, ['SIGNED_IN', 'SIGNED_OUT'])
  assert.equal((await other.auth.me()).email, address, 'the other session is unaffected')
})

test('password change expires other sessions', { skip }, async () => {
  const { c, email: address } = await user('pw')
  const other = client()
  await other.auth.login({ email: address, password })
  /** @type {string[]} */
  const events = []
  other.auth.onAuthChange((e) => events.push(e))

  await c.auth.changePassword({ currentPassword: password, newPassword: 'dev-p4ssw0rd!2' })
  await c.auth.me() // still valid
  await assert.rejects(other.auth.me(), AuthenticationError)
  assert.deepEqual(events, ['SESSION_EXPIRED'])
  assert.equal(await other.auth.token(), null)

  await assert.rejects(client().auth.login({ email: address, password }), (/** @type {any} */ e) => e.code === 'invalid_credentials')
  await client().auth.login({ email: address, password: 'dev-p4ssw0rd!2' })
})

test('revoke a session, log out everywhere, delete the account', { skip }, async () => {
  const { c, email: address } = await user('multi')
  const phone = client()
  await phone.auth.login({ email: address, password })
  const phoneSession = (await c.auth.sessions()).find((x) => !x.current)
  assert.ok(phoneSession)
  await c.auth.revokeSession(phoneSession.id)
  await assert.rejects(phone.auth.me(), AuthenticationError)

  const tablet = client()
  await tablet.auth.login({ email: address, password })
  await c.auth.logoutAll()
  await assert.rejects(tablet.auth.me(), AuthenticationError)

  const again = client()
  await again.auth.login({ email: address, password })
  await assert.rejects(again.auth.deleteAccount({ password: 'dev-p4ssw0rd!0' }), AuthenticationError)
  await again.auth.deleteAccount({ password })
  await assert.rejects(client().auth.login({ email: address, password }), AuthenticationError)
})

test('repeated failed logins are throttled', { skip }, async () => {
  const victim = email('victim')
  for (let i = 0; i < 5; i++) {
    await assert.rejects(client().auth.login({ email: victim, password: 'dev-p4ssw0rd!0' }), AuthenticationError)
  }
  await assert.rejects(client().auth.login({ email: victim, password: 'dev-p4ssw0rd!0' }), (/** @type {any} */ e) => {
    assert.ok(e instanceof RetryableError)
    assert.equal(e.status, 429)
    assert.ok(e.retryAfter !== undefined && e.retryAfter > 0 && e.retryAfter <= 2000, `retryAfter ${e.retryAfter}`)
    return true
  })
})
