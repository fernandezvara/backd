import { createClient } from '../../src/index.js'

export const url = process.env.BACKD_URL ?? ''
export const apiKey = process.env.BACKD_API_KEY ?? ''
export const inviteApiKey = process.env.BACKD_INVITE_API_KEY ?? ''

/** Set when there is no server to test against. */
export const skip = url === '' ? 'BACKD_URL not set; run `make js-integration`' : false

const run = Date.now().toString(36)
let n = 0
/** A unique email for this run. @param {string} name */
export const email = (name) => `${name}.${run}.${n++}@example.com`

export const password = 'dev-p4ssw0rd!'

/**
 * A client for a realm, with sessions in memory.
 * @param {Partial<import('../../src/index.js').ClientOptions>} [opts]
 */
export const client = (opts = {}) => createClient({ url, realm: 'itest', ...opts })

/** A client that signed up a new user. @param {string} name */
export async function user(name) {
  const c = client()
  const s = await c.auth.signup({ email: email(name), password })
  return { c, id: s.user.id, email: s.user.email }
}
