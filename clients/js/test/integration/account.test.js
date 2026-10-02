import { test } from 'node:test'
import assert from 'node:assert/strict'

import { AuthenticationError, ValidationError } from '../../src/index.js'
import { email, linkToken, mailApiKey, mailClient, password, skip } from './env.js'

const admin = () => mailClient({ apiKey: mailApiKey }).admin

test('sign-up sends a verification email; its token verifies the address, resend sends another', { skip }, async () => {
  const c = mailClient()
  const address = email('verify')
  const s = await c.auth.signup({ email: address, password, redirectTo: 'http://localhost:5173/verified' })
  assert.equal(s.user.email_verified, false)
  const first = await linkToken('verify-email', address)
  await c.auth.resendVerification({ email: address })
  const second = await linkToken('verify-email', address, { after: first.id })
  assert.notEqual(second.token, first.token)
  await c.auth.resendVerification({ email: 'nobody.' + address }) // the same answer for an unknown address

  await c.auth.verifyEmail(second.token)
  assert.equal((await c.auth.me()).email_verified, true)
  await assert.rejects(c.auth.verifyEmail(second.token), (/** @type {any} */ e) => e instanceof ValidationError && e.code === 'invalid_token')
  await assert.rejects(c.auth.verifyEmail('nope'), (/** @type {any} */ e) => e.code === 'invalid_token')
})

test('password reset: request, token, new password; every session ends', { skip }, async () => {
  const c = mailClient()
  const address = email('reset')
  await c.auth.signup({ email: address, password })
  await c.auth.requestPasswordReset({ email: address })
  await c.auth.requestPasswordReset({ email: 'nobody.' + address })
  const { token } = await linkToken('reset-password', address)

  await assert.rejects(c.auth.resetPassword({ token, password: 'short' }), (/** @type {any} */ e) => e instanceof ValidationError && e.details[0].path === 'password')
  await c.auth.resetPassword({ token, password: password + '2' }) // the refused password left the token usable
  await assert.rejects(c.auth.me(), AuthenticationError) // the session from before the reset ended
  await assert.rejects(c.auth.resetPassword({ token, password: password + '3' }), (/** @type {any} */ e) => e.code === 'invalid_token')
  await assert.rejects(mailClient().auth.login({ email: address, password }), AuthenticationError)
  const fresh = mailClient()
  assert.equal((await fresh.auth.login({ email: address, password: password + '2' })).user.email_verified, true) // the token proved the mailbox
})

test('email change: confirm at the new address, undo from the old one', { skip }, async () => {
  const c = mailClient()
  const old = email('old')
  const next = email('new')
  await c.auth.signup({ email: old, password })
  await assert.rejects(c.auth.requestEmailChange({ newEmail: next, password: 'wrong-password-1' }), AuthenticationError)
  await c.auth.requestEmailChange({ newEmail: next, password })
  const confirm = await linkToken('change-email', next)
  await c.auth.confirmEmailChange(confirm.token)
  await assert.rejects(c.auth.me(), AuthenticationError) // the change ended the sessions
  const moved = mailClient()
  assert.equal((await moved.auth.login({ email: next, password })).user.email, next)
  await assert.rejects(mailClient().auth.login({ email: old, password }), AuthenticationError)

  // The old address is told, and can undo it; the password is unusable until reset.
  const undo = await linkToken('email-changed', old)
  await mailClient().auth.revertEmailChange(undo.token)
  await assert.rejects(mailClient().auth.login({ email: old, password }), AuthenticationError)
  const reset = await linkToken('reset-password', old)
  await mailClient().auth.resetPassword({ token: reset.token, password: password + '2' })
  assert.equal((await mailClient().auth.login({ email: old, password: password + '2' })).user.email, old)
})

test('an emailed invitation creates a verified account; an administrator can change an address', { skip }, async () => {
  const invited = email('invited')
  const sent = await admin().invitations.send({ email: invited, redirectTo: 'http://localhost:5173/verified', locale: 'es' })
  assert.equal(sent.sent, true)
  assert.equal('token' in sent, false)
  const { token } = await linkToken('invitation', invited)
  await mailClient().auth.acceptInvitation({ token, password, locale: 'es' })
  const c = mailClient()
  const s = await c.auth.login({ email: invited, password })
  assert.equal(s.user.email_verified, true)
  await assert.rejects(mailClient().auth.acceptInvitation({ token, password }), (/** @type {any} */ e) => e.code === 'invalid_token')

  const moved = email('moved')
  const user = await admin().users.changeEmail(s.user.id, moved)
  assert.equal(user.email, moved)
  assert.equal(user.email_verified, true)
  await assert.rejects(c.auth.me(), AuthenticationError)
  const undo = await linkToken('email-changed', invited)
  assert.ok(undo.token)
})
