import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'

import { email, mailClient, mailCode, password, skip } from './env.js'

const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'

/** The TOTP code (SHA-1, 6 digits, 30 s) of a base32 secret at a time. @param {string} secret @param {number} at */
function totp(secret, at = Date.now()) {
  let bits = ''
  for (const ch of secret.toUpperCase().replace(/=+$/, '')) bits += B32.indexOf(ch).toString(2).padStart(5, '0')
  const key = Buffer.from(bits.match(/.{8}/g)?.map((b) => parseInt(b, 2)) ?? [])
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 30000)))
  const h = createHmac('sha1', key).update(counter).digest()
  const o = h[h.length - 1] & 0xf
  const n = (h.readUInt32BE(o) & 0x7fffffff) % 1_000_000
  return String(n).padStart(6, '0')
}

test('a second factor: enrol, sign in with a code, then with a recovery code', { skip }, async () => {
  const address = email('mfa')
  const c = mailClient()
  await c.auth.signup({ email: address, password })
  // Turning it on needs a fresh proof.
  await assert.rejects(c.auth.mfa.setup(), (/** @type {any} */ e) => e.code === 'reauth_required')
  await c.auth.reauth({ method: 'password', password })
  const setup = await c.auth.mfa.setup()
  assert.match(setup.otpauth_uri, /^otpauth:\/\/totp\//)
  const done = await c.auth.mfa.confirm({ setupId: setup.setup_id, code: totp(setup.secret) })
  assert.equal(done.recovery_codes.length, 10)
  assert.equal((await c.auth.mfa.status()).enabled, true)

  // The password alone is not a session any more.
  const other = mailClient()
  const step = /** @type {any} */ (await other.auth.signIn({ email: address, password }))
  assert.equal(step.mfaRequired, true)
  assert.equal(await other.auth.token(), null)
  await assert.rejects(other.auth.verifyMfa(step.challenge, 'totp', '000000'), (/** @type {any} */ e) => e.code === 'invalid_code')
  // (The time step that enrolled is spent: the next one signs in.)
  const session = await other.auth.verifyMfa(step.challenge, 'totp', totp(setup.secret, Date.now() + 30000))
  assert.ok(session.token)

  const third = mailClient()
  const step2 = /** @type {any} */ (await third.auth.signIn({ email: address, password }))
  const viaRecovery = await third.auth.verifyMfa(step2.challenge, 'recovery_code', done.recovery_codes[0])
  assert.equal(viaRecovery.recovery_codes_left, 9)
})

test('magic link: a new account by code, then a second factor is asked for', { skip }, async () => {
  const address = email('link')
  const c = mailClient()
  const asked = await c.auth.magicLink.request(address, { redirectTo: 'http://localhost:5173/in' })
  assert.ok(asked.requestId)
  const { code } = await mailCode('magic-link', address)
  await assert.rejects(c.auth.magicLink.verifyCode('000000'), (/** @type {any} */ e) => e.code === 'invalid_code')
  const s = /** @type {any} */ (await c.auth.magicLink.verifyCode(code))
  assert.equal(s.new_user, true)
  assert.ok(await c.auth.token())
  const me = await c.auth.me()
  assert.deepEqual(me.sign_in_methods, ['magic_link'])
  assert.deepEqual(me.reauth_methods, ['email_code'])

  // Without a password the proof is a code by email, and it lets the user set one.
  await c.auth.requestReauthCode()
  const proof = await mailCode('reauth-code', address)
  await c.auth.reauth({ method: 'email_code', code: proof.code })
  await c.auth.password.set({ newPassword: password })
  assert.equal((await mailClient().auth.login({ email: address, password })).user.email, address)
})
