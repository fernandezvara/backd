import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'
import { signBody } from '../examples/workshop/lib/sign.js'

test('signBody is the HMAC-SHA256 of the raw body, as sha256=<hex>', async () => {
  const body = '{"id":"evt_1","type":"payment.succeeded","order_id":"o1"}'
  const want = `sha256=${createHmac('sha256', 'whsec_test').update(body).digest('hex')}`
  assert.equal(await signBody('whsec_test', body), want)
  assert.notEqual(await signBody('other', body), want)
  assert.notEqual(await signBody('whsec_test', `${body} `), want)
})
