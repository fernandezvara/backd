// Panel 2: a privileged, idempotent refund (refund), which queues a receipt
// through an internal function (refund_receipt).
//
// A customer's rules can't mark an order refunded, so refund is a function
// with admin access, for staff only. Its Idempotency-Key makes a retry return
// the first answer instead of refunding twice; the order change and the refund
// record are one transaction; and it hands the receipt to an internal function
// that nobody can call over HTTP.

/** @param {{ db: any, backd: any }} deps */
export function refundPanel({ db, backd }) {
  return {
    refundOrderId: '',
    refundReason: 'damaged',
    refundKey: '',
    attempts: [], // { label, ok, text }
    receipt: null, // { job, status, text }

    refundKeyFor(orderId) {
      return orderId ? `refund-${orderId}` : ''
    },
    pickOrder() {
      this.refundKey = this.refundKeyFor(this.refundOrderId)
    },

    async _refund(label, orderId, key) {
      try {
        const out = await db.fn('refund', { order_id: orderId, reason: this.refundReason }, { idempotencyKey: key })
        this.attempts = [{ label, ok: true, text: `refund ${out.refund_id} for ${this.money(out.amount)}`, out }, ...this.attempts]
        return out
      } catch (err) {
        this.attempts = [{ label, ok: false, text: `${err.status} ${err.code}: ${err.message}` }, ...this.attempts]
        return null
      }
    },

    refund() {
      const orderId = this.refundOrderId
      const key = this.refundKey || this.refundKeyFor(orderId)
      return this.step(`Refund ${orderId}`, async () => {
        const out = await this._refund(`Refund with key ${key}`, orderId, key)
        if (out) await this.watchReceipt(out.receipt_job)
      })
    },

    // The same key and input: the first answer comes back, nothing is refunded twice.
    refundAgain() {
      const orderId = this.refundOrderId
      const key = this.refundKey || this.refundKeyFor(orderId)
      return this.step(`Refund ${orderId} again, same key`, () => this._refund(`Same key ${key} again`, orderId, key))
    },

    // A different key says "this is another refund": the order is already refunded.
    refundNewKey() {
      const orderId = this.refundOrderId
      return this.step(`Refund ${orderId} with a new key`, () => this._refund('A new key', orderId, `${this.refundKeyFor(orderId)}-${Date.now()}`))
    },

    // Two identical requests at once: one runs, the other waits or replays.
    refundRace() {
      const orderId = this.refundOrderId
      const key = `${this.refundKeyFor(orderId)}-race-${Date.now()}`
      return this.step(`Refund ${orderId} twice at once`, async () => {
        await Promise.all([this._refund('Race, request 1', orderId, key), this._refund('Race, request 2', orderId, key)])
      })
    },

    // The receipt is a job the refund queued: watch it finish.
    async watchReceipt(jobId) {
      if (!jobId) return
      this.receipt = { job: jobId, status: 'queued', text: '' }
      for (let i = 0; i < 40; i++) {
        const job = await backd.request({ method: 'GET', path: ['main', '_jobs', jobId] }).then((r) => r.data)
        this.receipt = { job: jobId, status: job.status, text: '' }
        if (job.status === 'done') {
          const page = await db.collection('receipts').list({ where: { refund_id: this.attempts.find((a) => a.out)?.out.refund_id }, limit: 1 }).catch(() => ({ items: [] }))
          this.receipt = { job: jobId, status: 'done', text: page.items[0]?.text ?? JSON.stringify(job.result?.output ?? job.result) }
          return
        }
        await new Promise((r) => setTimeout(r, 600))
      }
    },
  }
}
