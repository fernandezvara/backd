// Panel 3: a webhook from a payment provider (payment_webhook).
//
// The provider has no backd session, so the function accepts anonymous calls
// and is itself the only check: it verifies the HMAC signature of the raw body,
// ignores an event it already handled (the provider retries), and marks the
// order paid with admin access, which no customer rule allows.
import { signBody } from '../lib/sign.js'

// The secret the provider and the function share. In a real deployment it
// never leaves the provider and backd's encrypted secrets.
const SHARED_SECRET = 'whsec_test'

/** @param {{ backd: any, fetchRaw: typeof fetch, ACCOUNTS: any[] }} deps */
export function webhookPanel({ backd, fetchRaw, ACCOUNTS }) {
  const url = `${window.location.origin}/v1/workshop/main/_func/payment_webhook`
  return {
    hookOrderId: '',
    hookEventId: `evt_${Date.now().toString(36)}`,
    hookResults: [], // { label, ok, status, text }
    secretState: '', // what the operator step reported

    // Sends an event the way the provider would. `secret` is what it signs with.
    async _sendEvent(label, secret, eventId = this.hookEventId) {
      const body = JSON.stringify({ id: eventId, type: 'payment.succeeded', order_id: this.hookOrderId })
      const res = await fetchRaw(url, {
        method: 'POST',
        headers: { 'x-signature': await signBody(secret, body) },
        body,
      })
      const text = (await res.text()).trim()
      this.hookResults = [{ label, status: res.status, ok: res.ok, text: text.startsWith('{') ? this.errorText(text) : text }, ...this.hookResults]
      if (res.ok && this.orders.some((o) => o.id === this.hookOrderId)) await this.loadOrders()
      return res.status
    },

    errorText(text) {
      try {
        const e = JSON.parse(text).error
        return `${e.code}: ${e.message}`
      } catch {
        return text
      }
    },

    sendEvent() {
      return this.step(`Webhook: payment.succeeded for ${this.hookOrderId}`, async () => {
        const status = await this._sendEvent('Signed event', SHARED_SECRET)
        if (status === 500) this.secretState = 'The function answered 500: its secret isn\'t set yet. Use "Set the shared secret" below.'
      })
    },
    sendWrongSignature() {
      return this.step('Webhook: wrong signature', () => this._sendEvent('Signed with the wrong secret', 'not-the-secret'))
    },
    sendAgain() {
      return this.step('Webhook: the same event again', () => this._sendEvent('The same event again (the provider retries)', SHARED_SECRET))
    },
    newEvent() {
      this.hookEventId = `evt_${Date.now().toString(36)}`
    },

    // The operator (an administrator) sets the secret the function declares.
    // Signs in as the operator and back, so both show in the inspector.
    setSharedSecret() {
      const me = this.account
      return this.step('Operator: set the shared secret', async () => {
        await this._signIn(ACCOUNTS.find((a) => a.key === 'operator'))
        const known = (await backd.admin.secrets.list()).some((s) => s.name === 'PAYMENT_WEBHOOK_SECRET' && s.database === 'main')
        if (!known) await backd.admin.secrets.set('PAYMENT_WEBHOOK_SECRET', SHARED_SECRET, { database: 'main' })
        this.secretState = known ? 'The secret was already set.' : 'Secret set. Functions see a new value within about a minute.'
        if (me) await this._signIn(me)
        else {
          await backd.auth.logout().catch(() => {})
          this.user = null
          this.account = null
        }
      })
    },
  }
}
