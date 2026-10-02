// Panel 6: email verification, with the delivery function of step 6's text.
//
// A visitor signs up (this realm lets anyone in at once; a realm can require
// verification first, as the expenses examples do), backd queues a
// verify-email message, a worker renders it and email-capture stores it in
// the outbox, and the link in it verifies the address. The visitor has its
// own client with no stored session, so the tour's signed-in account stays.
import { createClient, memoryStorage } from '@backd/client'

const PASSWORD = 'dev-p4ssw0rd!'

/** @param {{ backd: any, fetch: typeof fetch, realm: string }} deps */
export function verifyPanel({ backd, fetch, realm }) {
  const outbox = backd.db('notifications').collection('outbox')
  let visitor = null // the client of the visitor

  return {
    /** @type {null | { email: string, verified: boolean, mail: null | { subject: string, link: string }, done: boolean }} */
    vf: null,

    verifySignUp() {
      return this.step('Sign up a visitor', async () => {
        visitor = createClient({ url: window.location.origin, realm, fetch, storage: memoryStorage() })
        const email = `visitor-${Date.now().toString(36)}@example.com`
        const session = await visitor.auth.signup({ email, password: PASSWORD })
        this.vf = { email, verified: session.user.email_verified, mail: null, done: false }
      })
    },

    // The message is made by a worker, so it takes a moment.
    verifyFindMail() {
      return this.step('Look for the email in the outbox', async () => {
        for (let i = 0; i < 40; i++) {
          const page = await outbox.list({ orderBy: '-_meta.created_at', limit: 50 })
          const mail = page.items.find((m) => m.kind === 'verify-email' && m.to.includes(this.vf.email))
          if (mail) {
            this.vf = { ...this.vf, mail: { subject: mail.subject, link: mail.link } }
            return
          }
          await new Promise((r) => setTimeout(r, 500))
        }
        throw new Error('no email yet: is a worker running?')
      })
    },

    // What an app with its own page does with the token in the link; backd's
    // hosted page (the link itself) does the same with a button.
    verifyWithToken() {
      return this.step('Verify the address with the token (POST /_auth/verify-email)', async () => {
        const token = new URL(this.vf.mail.link).searchParams.get('token')
        await visitor.request({ method: 'POST', path: ['_auth', 'verify-email'], body: { token }, auth: false })
        this.vf = { ...this.vf, done: true }
      })
    },

    verifyCheck() {
      return this.step('Read the visitor', async () => {
        const me = await visitor.auth.me()
        this.vf = { ...this.vf, verified: me.email_verified }
      })
    },
  }
}
