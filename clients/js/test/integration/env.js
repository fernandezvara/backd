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

export const filesApiKey = process.env.BACKD_FILES_API_KEY ?? ''
/** The files realm: MinIO behind it, links signed for the host. @param {Partial<import('../../src/index.js').ClientOptions>} [opts] */
export const filesClient = (opts = {}) => createClient({ url, realm: 'files', ...opts })

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

// The mail realm: email goes through the dev-only email-capture function, so
// the links of the account emails can be read from its outbox.
export const mailApiKey = process.env.BACKD_MAIL_API_KEY ?? ''
/** @param {Partial<import('../../src/index.js').ClientOptions>} [opts] */
export const mailClient = (opts = {}) => createClient({ url, realm: 'mail', ...opts })

/**
 * Waits for an email of a kind to an address and returns the token in its
 * link (there is no way to get it elsewhere: backd stores only a hash).
 * @param {string} kind
 * @param {string} address
 * @param {{ after?: string }} [opts] after: ignore emails up to this id (a later one of the same kind)
 */
export async function linkToken(kind, address, { after = '' } = {}) {
  const outbox = mailClient().db('notifications').collection('outbox')
  for (let i = 0; i < 100; i++) {
    const page = await outbox.list({ orderBy: '-_meta.created_at', limit: 100 })
    const mail = page.items.find((m) => m.kind === kind && m.to.includes(address.toLowerCase()) && m.id > after && m.link)
    if (mail) return { token: new URL(mail.link).searchParams.get('token') ?? '', id: mail.id }
    await new Promise((resolve) => setTimeout(resolve, 100)) // a worker delivers it
  }
  throw new Error(`no ${kind} email for ${address} in the outbox`)
}
