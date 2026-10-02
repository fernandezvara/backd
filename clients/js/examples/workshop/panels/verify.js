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

  // The message of a kind to an address, once a worker has delivered it.
  async function waitFor(kind, address) {
    for (let i = 0; i < 40; i++) {
      const page = await outbox.list({ orderBy: '-_meta.created_at', limit: 50 })
      const mail = page.items.find((m) => m.kind === kind && m.to.includes(address))
      if (mail) return mail
      await new Promise((r) => setTimeout(r, 500))
    }
    throw new Error(`no ${kind} email yet: is a worker running?`)
  }

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

    // Password reset, for the same visitor ("I forgot my password").
    /** @type {null | { mail: null | { subject: string, link: string }, done: boolean, oldSessionEnded: boolean | null, signedIn: boolean }} */
    rs: null,

    resetAsk() {
      return this.step('Ask for a password reset (always answers 202)', async () => {
        await visitor.request({ method: 'POST', path: ['_auth', 'reset-password', 'request'], body: { email: this.vf.email }, auth: false })
        this.rs = { mail: null, done: false, oldSessionEnded: null, signedIn: false }
      })
    },

    resetFindMail() {
      return this.step('Look for the reset email in the outbox', async () => {
        for (let i = 0; i < 40; i++) {
          const page = await outbox.list({ orderBy: '-_meta.created_at', limit: 50 })
          const mail = page.items.find((m) => m.kind === 'reset-password' && m.to.includes(this.vf.email))
          if (mail) {
            this.rs = { ...this.rs, mail: { subject: mail.subject, link: mail.link } }
            return
          }
          await new Promise((r) => setTimeout(r, 500))
        }
        throw new Error('no email yet: is a worker running?')
      })
    },

    resetApply() {
      return this.step('Set a new password with the token (POST /_auth/reset-password)', async () => {
        const token = new URL(this.rs.mail.link).searchParams.get('token')
        await visitor.request({ method: 'POST', path: ['_auth', 'reset-password'], body: { token, password: `${PASSWORD}2` }, auth: false })
        // The reset ended every session, the visitor's own included.
        let ended = false
        try {
          await visitor.auth.me()
        } catch (err) {
          ended = err.status === 401
        }
        this.rs = { ...this.rs, done: true, oldSessionEnded: ended }
      })
    },

    resetLogin() {
      return this.step('Log in with the new password', async () => {
        await visitor.auth.login({ email: this.vf.email, password: `${PASSWORD}2` })
        this.rs = { ...this.rs, signedIn: true }
        await this.verifyCheck()
      })
    },

    // Changing the address: asked while signed in, confirmed at the new
    // address, undone from the old one.
    /** @type {null | { newEmail: string, confirmed: boolean, sessionEnded: boolean | null, undone: boolean }} */
    ch: null,

    changeAsk() {
      return this.step('Ask to change the email address (needs the password)', async () => {
        const newEmail = `moved-${Date.now().toString(36)}@example.com`
        await visitor.auth.requestEmailChange({ newEmail, password: `${PASSWORD}2` })
        this.ch = { newEmail, confirmed: false, sessionEnded: null, undone: false }
      })
    },

    changeConfirm() {
      return this.step('Confirm at the new address (the link sent there)', async () => {
        const mail = await waitFor('change-email', this.ch.newEmail)
        await visitor.auth.confirmEmailChange(new URL(mail.link).searchParams.get('token'))
        let ended = false
        try {
          await visitor.auth.me()
        } catch (err) {
          ended = err.status === 401
        }
        this.ch = { ...this.ch, confirmed: true, sessionEnded: ended }
      })
    },

    changeUndo() {
      return this.step('Undo it from the old address (the link in email-changed)', async () => {
        const mail = await waitFor('email-changed', this.vf.email)
        await visitor.auth.revertEmailChange(new URL(mail.link).searchParams.get('token'))
        this.ch = { ...this.ch, undone: true }
      })
    },

    // An administrator emails an invitation: nobody holds its token.
    /** @type {null | { email: string, subject: string, link: string, accepted: boolean }} */
    inv: null,

    inviteSend() {
      return this.step('Invite someone by email (admin: invitations.send)', async () => {
        const email = `invited-${Date.now().toString(36)}@example.com`
        const sent = await backd.admin.invitations.send({ email, redirectTo: `${window.location.origin}/example/workshop/` })
        const mail = await waitFor('invitation', email)
        this.inv = { email, subject: mail.subject, link: mail.link, accepted: false, sent: sent.sent }
      })
    },

    inviteAccept() {
      return this.step('Accept the invitation with the token (POST /_auth/accept-invitation)', async () => {
        const guest = createClient({ url: window.location.origin, realm, fetch, storage: memoryStorage() })
        await guest.auth.acceptInvitation({ token: new URL(this.inv.link).searchParams.get('token'), password: PASSWORD })
        const session = await guest.auth.login({ email: this.inv.email, password: PASSWORD })
        this.inv = { ...this.inv, accepted: true, verified: session.user.email_verified }
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
