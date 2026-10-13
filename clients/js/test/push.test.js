import { test } from 'node:test'
import assert from 'node:assert/strict'

import { createClient, ForbiddenError } from '../src/index.js'
import { mockFetch, errorBody, session } from './helpers.js'

const memory = () => {
  /** @type {string | null} */
  let held = null
  return { get: () => held, set: (/** @type {string} */ v) => (held = v), remove: () => (held = null) }
}
/** @param {ReturnType<typeof mockFetch>} m */
const clientOf = (m) => createClient({ url: 'https://api.test', realm: 'acme', fetch: m.fetch, oauthStorage: memory() })

const device = { id: 'd1', platform: 'android', name: 'Pixel', created_at: '2026-10-14T10:00:00.000Z', last_seen: '2026-10-14T10:00:00.000Z' }

test('register posts the device, keeps its id and unregister removes it', async () => {
  const m = mockFetch([{ body: device }, { status: 204 }])
  const c = clientOf(m)
  const d = await c.push.register({ platform: 'android', token: 'fcm-1', name: 'Pixel' })
  assert.equal(d.id, 'd1')
  assert.equal(c.push.deviceId, 'd1')
  assert.deepEqual(m.calls[0].body, { platform: 'android', token: 'fcm-1', name: 'Pixel' })
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/devices')
  assert.equal(await c.push.unregister(), true)
  assert.equal(m.calls[1].method, 'DELETE')
  assert.equal(m.calls[1].url.pathname, '/v1/acme/_auth/devices/d1')
  assert.equal(c.push.deviceId, undefined)
  // With nothing registered there is nothing to remove, and nothing is called.
  assert.equal(await c.push.unregister(), false)
  assert.equal(m.remaining(), 0)
})

test('the name is only sent when given, and devices lists without tokens', async () => {
  const m = mockFetch([{ body: device }, { body: { items: [device] } }])
  const c = clientOf(m)
  await c.push.register({ platform: 'ios', token: 'apns-1' })
  assert.deepEqual(m.calls[0].body, { platform: 'ios', token: 'apns-1' })
  assert.deepEqual(await c.push.devices(), [device])
})

test('errors keep their class: an API key can not register devices', async () => {
  const m = mockFetch([{ status: 403, body: errorBody('forbidden') }])
  const c = clientOf(m)
  await assert.rejects(c.push.register({ platform: 'android', token: 'x' }), ForbiddenError)
})

test('ending the session forgets the device', async () => {
  const m = mockFetch([{ body: session('bds_x') }, { body: device }, { status: 204 }])
  const c = clientOf(m)
  await c.auth.login({ email: 'a@x.io', password: 'p' })
  await c.push.register({ platform: 'android', token: 'x' })
  assert.equal(c.push.deviceId, 'd1')
  await c.auth.logout()
  assert.equal(c.push.deviceId, undefined)
})

test('registerOnLaunch registers now when signed in, after every sign-in, and stops', async () => {
  const m = mockFetch([{ body: session('bds_1') }, { body: device }, { body: { ...device, id: 'd2' } }])
  const c = clientOf(m)
  let tokens = 0
  /** @type {unknown[]} */
  const errors = []
  const stop = c.push.registerOnLaunch(async () => ({ platform: 'android', token: `t${++tokens}` }), { onError: (e) => errors.push(e) })
  // Signed out: nothing is registered at launch.
  await new Promise((r) => setTimeout(r, 5))
  assert.equal(m.calls.length, 0)
  await c.auth.login({ email: 'a@x.io', password: 'p' }) // → register
  await new Promise((r) => setTimeout(r, 5))
  assert.equal(m.calls.length, 2)
  assert.deepEqual(m.calls[1].body, { platform: 'android', token: 't1' })
  assert.equal(c.push.deviceId, 'd1')
  stop()
  assert.deepEqual(errors, [])
})

test('registerOnLaunch with no token yet registers nothing, and reports failures', async () => {
  const m = mockFetch([{ body: session('bds_1') }, { body: session('bds_2') }, { status: 400, body: errorBody('platform_not_enabled') }])
  const c = clientOf(m)
  /** @type {unknown[]} */
  const errors = []
  /** @type {{ platform: 'android' | 'ios' | 'web', token: string } | null} */
  let answer = null
  c.push.registerOnLaunch(async () => answer, { onError: (e) => errors.push(e) })
  await c.auth.login({ email: 'a@x.io', password: 'p' })
  await new Promise((r) => setTimeout(r, 5))
  assert.equal(m.calls.length, 1) // only the login: the app has no token yet
  answer = { platform: 'ios', token: 'x' }
  await c.auth.login({ email: 'a@x.io', password: 'p' })
  await new Promise((r) => setTimeout(r, 5))
  assert.equal(errors.length, 1)
  assert.equal(/** @type {any} */ (errors[0]).code, 'platform_not_enabled')
})

/** A service worker registration whose pushManager remembers what it was asked. */
function registration() {
  /** @type {any[]} */
  const asked = []
  return {
    asked,
    pushManager: {
      subscribe: async (/** @type {any} */ options) => {
        asked.push(options)
        return { toJSON: () => ({ endpoint: 'https://fcm.googleapis.com/send/abc', keys: { p256dh: 'AAAA', auth: 'BBBB' } }) }
      },
    },
  }
}

test('subscribeWeb fetches the key, subscribes and registers the subscription', async () => {
  const m = mockFetch([{ body: { public_key: 'BEl62iUYgUivxIkv69yViEuiBIa-Ib9-SkvMeAtA3LFgDzkrxZJjSgSnfckjBJuBkr3qBUYIHBQFLXYp5Nksh8U' } }, { body: { ...device, platform: 'web' } }])
  const c = clientOf(m)
  const reg = registration()
  const notification = { permission: 'default', requestPermission: async () => 'granted' }
  const out = await c.push.subscribeWeb({ registration: reg, name: 'Laptop', notification })
  assert.equal(out.permission, 'granted')
  assert.equal(out.device?.platform, 'web')
  assert.equal(m.calls[0].url.pathname, '/v1/acme/_auth/devices/web-key')
  assert.equal(reg.asked[0].userVisibleOnly, true)
  assert.ok(reg.asked[0].applicationServerKey instanceof Uint8Array)
  assert.equal(reg.asked[0].applicationServerKey.length, 65) // an uncompressed P-256 point
  assert.equal(reg.asked[0].applicationServerKey[0], 4)
  const sent = m.calls[1].body
  assert.equal(sent.platform, 'web')
  assert.deepEqual(JSON.parse(sent.token), { endpoint: 'https://fcm.googleapis.com/send/abc', keys: { p256dh: 'AAAA', auth: 'BBBB' } })
  assert.equal(sent.name, 'Laptop')
})

test('subscribeWeb does nothing when the permission is denied or unsupported', async () => {
  const m = mockFetch([])
  const c = clientOf(m)
  const reg = registration()
  assert.deepEqual(await c.push.subscribeWeb({ registration: reg, notification: { permission: 'denied', requestPermission: async () => 'denied' } }), { permission: 'denied', device: null })
  assert.deepEqual(await c.push.subscribeWeb({ registration: reg, notification: { permission: 'default', requestPermission: async () => 'denied' } }), { permission: 'denied', device: null })
  // Not asking, when the app says not to.
  assert.deepEqual(await c.push.subscribeWeb({ registration: reg, requestPermission: false, notification: { permission: 'default', requestPermission: async () => 'granted' } }), { permission: 'default', device: null })
  assert.deepEqual(await c.push.subscribeWeb({ registration: /** @type {any} */ ({}), notification: { permission: 'granted', requestPermission: async () => 'granted' } }), { permission: 'unsupported', device: null })
  assert.equal(m.calls.length, 0)
  assert.equal(reg.asked.length, 0)
})
