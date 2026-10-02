// Signs up a user in a realm that requires verified addresses, the way a
// person would with the local stack: the verification email lands in the
// realm's development outbox (the Mailbox app shows it), and its link is
// followed. Used by the hack scripts of the examples.
import { createClient, VerificationRequiredError } from '../src/index.js'

/**
 * @param {object} opts
 * @param {string} opts.url       The server, such as https://localhost:8443.
 * @param {string} opts.realm
 * @param {string} opts.email
 * @param {string} opts.password
 * @returns {Promise<import('../src/index.js').Client>} A client signed in as the new, verified user.
 */
export async function signupVerified({ url, realm, email, password }) {
  const c = createClient({ url, realm })
  try {
    await c.auth.signup({ email, password })
  } catch (err) {
    if (!(err instanceof VerificationRequiredError)) throw err
  }
  const token = await verificationToken(c, email)
  const res = await fetch(`${url}/v1/${realm}/_auth/verify-email`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  })
  if (res.status !== 204) throw new Error(`verifying ${email}: ${res.status} ${await res.text()}`)
  await c.auth.login({ email, password })
  return c
}

/** Waits for the verification email of an address and returns its token. */
async function verificationToken(/** @type {import('../src/index.js').Client} */ c, /** @type {string} */ email) {
  const outbox = c.db('notifications').collection('outbox')
  for (let attempt = 0; attempt < 100; attempt++) {
    const page = await outbox.list({ orderBy: '-_meta.created_at', limit: 100 })
    const mail = page.items.find((m) => m.kind === 'verify-email' && m.to.includes(email.toLowerCase()))
    if (mail?.link) return new URL(mail.link).searchParams.get('token') ?? ''
    await new Promise((resolve) => setTimeout(resolve, 100)) // a worker delivers it
  }
  throw new Error(`no verification email for ${email} in the outbox (is a worker running?)`)
}
