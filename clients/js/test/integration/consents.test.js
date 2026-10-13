import { test } from 'node:test'
import assert from 'node:assert/strict'

import { client, email, password, skip } from './env.js'

test('consents: texts, accepting at sign-up, withdrawing and the history', { skip }, async () => {
  const anon = client()
  const list = await anon.consents.current()
  const marketing = list.find((c) => c.name === 'marketing')
  assert.ok(marketing && marketing.required === false && marketing.current.version === '1')
  const text = await anon.consents.text('marketing', '1', 'en')
  assert.match(text.text, /withdraw at any time/)
  assert.equal(text.hash.length, 64)

  // An unknown consent at sign-up is refused, and no account is made.
  await assert.rejects(anon.auth.signup({ email: email('c'), password, consents: { cookies: '1' } }), (/** @type {any} */ e) => e.status === 400)

  const c = client()
  await c.auth.signup({ email: email('ada'), password, consents: { marketing: '1' } })
  let state = await c.consents.state()
  assert.equal(state.consents.marketing, '1')
  assert.equal(state.history?.[0].method, 'signup')
  assert.deepEqual(state.to_accept, [])

  state = await c.consents.withdraw('marketing')
  assert.equal(state.consents.marketing, undefined)
  state = await c.consents.accept({ marketing: '1' })
  assert.equal(state.consents.marketing, '1')
  assert.equal((await c.consents.state()).history?.map((h) => h.action).join(), 'accepted,withdrawn,accepted')

  await assert.rejects(c.consents.withdraw('nope'), (/** @type {any} */ e) => e.status === 404)
})
