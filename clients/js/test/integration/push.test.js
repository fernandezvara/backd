import { test } from 'node:test'
import assert from 'node:assert/strict'

import { ForbiddenError, NotFoundError } from '../../src/index.js'
import { client, skip, url, user } from './env.js'

/** Waits for a captured delivery of a send, as the Mailbox's Push tab reads them. @param {string} sendId */
async function captured(sendId) {
  for (let i = 0; i < 100; i++) {
    const res = await fetch(`${url}/v1/itest/_dev/push?limit=100`)
    const { items } = await res.json()
    const found = items.find((/** @type {any} */ e) => e.send_id === sendId)
    if (found) return found
    await new Promise((r) => setTimeout(r, 100)) // a worker delivers it
  }
  throw new Error(`no captured delivery of ${sendId}`)
}

test('a device registered with the client receives what a function pushes', { skip }, async () => {
  const ada = await user('ada')
  const token = `test-itest-${Date.now().toString(36)}`
  const d = await ada.c.push.register({ platform: 'android', token, name: 'Test phone' })
  assert.equal(d.platform, 'android')
  assert.equal(d.name, 'Test phone')
  assert.equal(ada.c.push.deviceId, d.id)
  assert.equal(JSON.stringify(d).includes(token), false, 'the token never comes back')
  assert.deepEqual((await ada.c.push.devices()).map((x) => x.id), [d.id])

  const sent = /** @type {any} */ (await ada.c.db('app').fn('push_hello', { name: 'Ana' }))
  assert.equal(sent.users, 1)
  assert.equal(sent.queued, 1)
  const entry = await captured(sent.send_id)
  assert.equal(entry.device_id, d.id)
  assert.equal(entry.status, 'sent')
  assert.equal(entry.message.title, 'Hello Ana')
  assert.equal(entry.message.link, 'itest://hello/Ana')
  assert.equal(entry.message.group, 'hello')

  assert.equal(await ada.c.push.unregister(), true)
  assert.deepEqual(await ada.c.push.devices(), [])
})

test('a device that signs out stops receiving, and tokens steer the capture provider', { skip }, async () => {
  const ada = await user('ada')
  await ada.c.push.register({ platform: 'android', token: `test-invalid-${Date.now().toString(36)}` })
  const sent = /** @type {any} */ (await ada.c.db('app').fn('push_hello', {}))
  const entry = await captured(sent.send_id)
  assert.equal(entry.status, 'invalid')
  // The dead token was removed by the delivery.
  for (let i = 0; i < 50 && (await ada.c.push.devices()).length > 0; i++) await new Promise((r) => setTimeout(r, 100))
  assert.deepEqual(await ada.c.push.devices(), [])

  await ada.c.push.register({ platform: 'android', token: `phone-${Date.now().toString(36)}` })
  assert.equal((await ada.c.push.devices()).length, 1)
  await ada.c.auth.logout()
  assert.equal(ada.c.push.deviceId, undefined)
})

test('there are no devices without a session, and no web key without a web platform', { skip }, async () => {
  const anon = client()
  await assert.rejects(anon.push.register({ platform: 'android', token: 'x' }), (/** @type {any} */ e) => e.status === 401)
  await assert.rejects(anon.push.devices(), (/** @type {any} */ e) => e.status === 401)
  const ada = await user('ada')
  await assert.rejects(ada.c.push.register({ platform: 'ios', token: 'x' }), (/** @type {any} */ e) => e.code === 'platform_not_enabled')
  await assert.rejects(ada.c.push.unregister('nope'), NotFoundError)
  void ForbiddenError
})
