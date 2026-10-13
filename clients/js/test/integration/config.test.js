import { test } from 'node:test'
import assert from 'node:assert/strict'

import { apiKey, client, password, email, skip, url } from './env.js'

/** An administrator changes the configuration through the admin API. @param {object} body */
async function change(body) {
  const base = `${url}/v1/itest/_admin/app-config`
  const headers = { Authorization: `Bearer ${apiKey}`, 'Content-Type': 'application/json' }
  const current = await (await fetch(base, { headers })).json()
  const res = await fetch(base, { method: 'PATCH', headers: { ...headers, 'If-Match': `"${current.version}"` }, body: JSON.stringify(body) })
  assert.equal(res.status, 200, await res.text())
}

test('app configuration: public and signed-in keys, revalidation and clearing', { skip }, async () => {
  await change({ reset: ['maintenance', 'features'] })

  const anon = client()
  const pub = await anon.config.load()
  assert.deepEqual(pub, { maintenance: { enabled: false, message: '' } }) // features need a session
  assert.equal(anon.config.getSync('features.export.enabled'), undefined)

  const c = client()
  await c.auth.signup({ email: email('cfg'), password })
  const all = await c.config.load()
  assert.equal(all.features.export.enabled, true)

  // A change reaches the client when it revalidates.
  await change({ set: { 'features.export.enabled': false } })
  /** @type {string[][]} */
  const changes = []
  c.config.on('change', (paths) => changes.push(paths))
  assert.equal(await c.config.get('features.export.enabled', { refresh: true }), false)
  assert.deepEqual(changes, [['features.export.enabled']])

  // Signing out drops what the user could read.
  await c.auth.logout()
  assert.equal(c.config.getSync('features.export.enabled'), undefined)
  await change({ reset: ['features'] })
})
