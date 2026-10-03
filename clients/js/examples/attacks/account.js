// Attacks the account flows the way an outsider could, over plain HTTP, on
// the expenses realm (open sign-up, verified email required, development
// outbox). Each line is something the second security review found or
// decided; the script is its regression suite and runs in CI with the other
// attack scripts (make example-attacks).
//
// It exits with 0 when every attack is stopped. A line marked ✗ is an attack
// that got through, or an answer that changed: fix backd, or, when the
// change is intended, the docs and this script.
//
//   make example                   # in another terminal
//   NODE_EXTRA_CA_CERTS=docker/certs/ca.crt node clients/js/examples/attacks/account.js
import { signupVerified } from '../verify-email.js'

const url = process.env.BACKD_URL ?? 'https://localhost:8443'
const realm = 'expenses'
const run = Date.now().toString(36)
const email = (/** @type {string} */ name) => `${name}.${run}@example.com`
const password = 'dev-p4ssw0rd!'

/** @param {string} path @param {{ method?: string, body?: unknown, token?: string, headers?: Record<string, string> }} [o] */
async function call(path, { method = 'POST', body, token, headers = {} } = {}) {
  const res = await fetch(`${url}/v1/${realm}${path}`, {
    method,
    redirect: 'manual',
    headers: {
      ...(body === undefined ? {} : { 'Content-Type': 'application/json' }),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...headers,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await res.text()
  /** @type {any} */ let json = null
  try { json = JSON.parse(text) } catch { /* not JSON */ }
  return { status: res.status, headers: res.headers, text, json, code: json?.error?.code }
}

let unexpected = 0
/** @param {string} what @param {boolean} ok @param {unknown} [detail] */
function stopped(what, ok, detail) {
  if (!ok) unexpected++
  console.log(`  ${ok ? '✓ stopped ' : '✗ NOT STOPPED'}  ${what}${ok ? '' : `\n                 got: ${typeof detail === 'string' ? detail : JSON.stringify(detail)}`}`)
}

console.log('Placeholder and look-alike addresses:')
const squat = await call('/_auth/signup', { body: { email: 'erased-000000000000000000000000@erased.invalid', password } })
stopped('signing up with an erased user\'s placeholder address (a squatter could block an erasure)', squat.status === 400, squat)
const reserved = await call('/_auth/signup', { body: { email: email('x').replace('example.com', 'mail.invalid'), password } })
stopped('signing up on any .invalid address', reserved.status === 400, reserved)

console.log('\nWhat sign-up and the email flows reveal:')
const taken = email('taken')
const first = await call('/_auth/signup', { body: { email: taken, password } })
const second = await call('/_auth/signup', { body: { email: taken, password: password + '2' } })
stopped('sign-up answers the same for a new and an existing address (no account enumeration)', first.status === second.status && first.code === second.code, { first: first.status, second: second.status })
const login = await call('/_auth/login', { body: { email: taken, password } })
stopped('an unverified address has no session', login.status === 401 || login.status === 403, login)
const known = await call('/_auth/reset-password/request', { body: { email: taken } })
const unknown = await call('/_auth/reset-password/request', { body: { email: email('nobody') } })
stopped('asking for a reset answers the same for an unknown address', known.status === unknown.status && known.status === 202, { known: known.status, unknown: unknown.status })
const resend = await call('/_auth/verify-email/resend', { body: { email: email('nobody2') } })
stopped('asking again for a verification email answers 202 for an unknown address', resend.status === 202, resend)

console.log('\nTokens:')
for (const [name, path, extra] of /** @type {const} */ ([
  ['verification', '/_auth/verify-email', {}],
  ['password reset', '/_auth/reset-password', { password: 'dev-p4ssw0rd!3' }],
  ['invitation', '/_auth/accept-invitation', { password: 'dev-p4ssw0rd!3' }],
])) {
  const r = await call(path, { body: { token: 'bdt_' + 'A'.repeat(43), ...extra } })
  stopped(`a made-up ${name} token is refused (${r.status} ${r.code ?? ''})`, r.status === 400, r)
}
const get = await call('/_auth/verify-email?token=' + 'A'.repeat(43), { method: 'GET' })
stopped('opening a link with a bad token only shows a page, as an error (a GET never changes anything)', get.status === 400 && /text\/html/.test(get.headers.get('content-type') ?? ''), get.status)
stopped('the page carries no-store and no-referrer', /no-store/.test(get.headers.get('cache-control') ?? '') && get.headers.get('referrer-policy') === 'no-referrer', Object.fromEntries(get.headers))
stopped('and a policy that blocks framing and scripts', /default-src 'none'/.test(get.headers.get('content-security-policy') ?? '') && /frame-ancestors 'none'/.test(get.headers.get('content-security-policy') ?? ''), get.headers.get('content-security-policy'))

console.log('\nWhat a signed-in user can reach:')
const eveAddress = email('eve')
await signupVerified({ url, realm, email: eveAddress, password })
const key = (await call('/_auth/login', { body: { email: eveAddress, password } })).json?.token
const mine = await call('/_auth/me', { method: 'GET', token: key })
stopped('eve is signed in (sanity)', mine.status === 200, mine)
for (const [what, path, method] of /** @type {const} */ ([
  ['listing users', '/_admin/users', 'GET'],
  ['erasing a user', '/_admin/users/000000000000000000000000', 'DELETE'],
  ['reading the audit trail', '/_admin/audit', 'GET'],
  ['listing jobs', '/_admin/jobs', 'GET'],
  ['calling a function by hand', '/_admin/functions/main/anything/invoke', 'POST'],
])) {
  const r = await call(path, { method, token: key, body: method === 'POST' ? {} : undefined })
  stopped(`a session can't use the admin API: ${what} (${r.status} ${r.code ?? ''})`, r.status === 403)
}
const anon = await call('/_admin/users/000000000000000000000000', { method: 'DELETE' })
stopped(`nobody can erase a user without credentials (${anon.status})`, anon.status === 401 || anon.status === 403)
const send = await call('/_email/send', { body: { to: ['victim@example.com'], subject: 'hi', text: 'hi' } })
stopped(`the internal route that sends custom emails isn't served on the public address (${send.status})`, send.status === 404 || send.status === 401 || send.status === 403)
const change = await call('/_auth/email', { token: key, body: { email: email('eve2'), password: 'wrong-password-1!' } })
stopped(`changing the address isn't possible where the realm doesn't allow it (${change.status})`, change.status === 404 || change.status === 403)
const mismatch = await call('/_auth/password', { token: key, body: { current_password: 'wrong-password-1!', new_password: 'dev-p4ssw0rd!9' } })
stopped(`changing the password needs the current one (${mismatch.status})`, mismatch.status === 401 || mismatch.status === 400)

console.log(unexpected === 0
  ? '\nEvery account attack above was stopped.'
  : `\n${unexpected} result(s) differ from what the review expects: see the lines marked ✗.`)
process.exit(unexpected === 0 ? 0 : 1)
