import { test } from 'node:test'
import assert from 'node:assert/strict'

import { email, password, skip, url } from './env.js'

const origin = 'http://localhost:5173' // in the realm's cors.origins
const base = `${url}/v1/itest`

/** @param {string} path @param {unknown} body @param {Record<string, string>} [headers] */
const post = (path, body, headers = {}) =>
  fetch(base + path, { method: 'POST', headers: { 'Content-Type': 'application/json', ...headers }, body: JSON.stringify(body) })

/** The cookie a response sets, as a `name=value` for the next request, and its attributes. @param {Response} res */
function setCookie(res) {
  const raw = res.headers.getSetCookie()[0] ?? ''
  return { pair: raw.split(';')[0], raw }
}

test('a session in an HttpOnly cookie', { skip }, async () => {
  const address = email('cookie')
  // Sign up with a token as usual, then sign in asking for a cookie.
  assert.equal((await post('/_auth/signup', { email: address, password })).status, 201)

  // Only from an allowed origin.
  assert.equal((await post('/_auth/login', { email: address, password, cookie: true })).status, 403)
  assert.equal((await post('/_auth/login', { email: address, password, cookie: true }, { Origin: 'https://evil.example' })).status, 403)

  const res = await post('/_auth/login', { email: address, password, cookie: true }, { Origin: origin })
  assert.equal(res.status, 200)
  const body = /** @type {any} */ (await res.json())
  assert.equal(body.token, undefined, 'the token stays out of the response')
  const { pair, raw } = setCookie(res)
  assert.match(pair, /^__Secure-backd_session=bds_/)
  for (const attribute of ['HttpOnly', 'Secure', 'SameSite=Lax', 'Path=/v1/itest']) assert.ok(raw.includes(attribute), `${attribute} in ${raw}`)
  assert.equal(res.headers.get('access-control-allow-credentials'), 'true')
  assert.equal(res.headers.get('access-control-allow-origin'), origin)

  // The cookie signs requests in; changing anything needs an allowed origin.
  const me = await fetch(base + '/_auth/me', { headers: { Cookie: pair } })
  assert.equal(me.status, 200)
  assert.equal(/** @type {any} */ (await me.json()).email, address)
  assert.equal((await fetch(base + '/app/posts', { method: 'POST', headers: { Cookie: pair, 'Content-Type': 'application/json' }, body: '{"title":"x"}' })).status, 403)
  const created = await fetch(base + '/app/posts', { method: 'POST', headers: { Cookie: pair, 'Content-Type': 'application/json', Origin: origin }, body: '{"title":"mine"}' })
  assert.equal(created.status, 201)

  // Logout clears the cookie and ends the session.
  const out = await post('/_auth/logout', undefined, { Cookie: pair, Origin: origin })
  assert.equal(out.status, 204)
  assert.match(setCookie(out).raw, /Max-Age=0|Expires=/i)
  assert.equal((await fetch(base + '/_auth/me', { headers: { Cookie: pair } })).status, 401)

  // A login that doesn't ask for a cookie still gets a token.
  const plain = /** @type {any} */ (await (await post('/_auth/login', { email: address, password })).json())
  assert.match(plain.token, /^bds_/)
})
