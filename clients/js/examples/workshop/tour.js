// Walks the workshop tour with the same client library the page uses, without
// a browser, and checks what each step of the tour says should happen: the
// tax computed on the server, a stranger's order that doesn't exist for you,
// a signed webhook and its replay, a staff-only idempotent refund that queues
// a receipt through an internal function, an async report, and what an
// operator can see and do. It is the tour's regression test.
//
//   make example                       # in another terminal, then:
//   make workshop-tour                 # or:
//   NODE_EXTRA_CA_CERTS=docker/certs/ca.crt node clients/js/examples/workshop/tour.js
//
// BACKD_URL overrides the server (default https://localhost:8443). It uses
// the demo accounts of the workshop realm (created on first use) and places
// new orders on every run, so it can run again and again. Exits 0 when every
// check passes, 1 otherwise.
import { createHmac } from 'node:crypto'
import { createClient, ForbiddenError, NotFoundError } from '../../src/index.js'

const url = process.env.BACKD_URL ?? 'https://localhost:8443'
const PASSWORD = 'dev-p4ssw0rd!'
const SECRET = 'whsec_test'

let failed = 0
/** @param {string} what @param {boolean} ok @param {string} [detail] */
function check(what, ok, detail = '') {
  if (!ok) failed++
  console.log(`  ${ok ? '✓' : '✗'} ${what}${ok || !detail ? '' : `\n      ${detail}`}`)
}
/** Runs an action that must be refused with this error class and code. */
async function refused(/** @type {string} */ what, /** @type {() => Promise<unknown>} */ action, /** @type {Function} */ Class, /** @type {string} */ code) {
  try {
    await action()
    check(what, false, 'it succeeded')
  } catch (err) {
    const e = /** @type {any} */ (err)
    check(`${what} (${e.status} ${e.code})`, err instanceof Class && (!code || e.code === code), `${e.name}: ${e.message}`)
  }
}
const heading = (/** @type {string} */ text) => console.log(`\n${text}`)

/** A client signed in as a demo account, which signs up the first time. */
async function signedIn(/** @type {string} */ email) {
  const backd = createClient({ url, realm: 'workshop' })
  try {
    await backd.auth.login({ email, password: PASSWORD })
  } catch (err) {
    if (/** @type {any} */ (err).status !== 401) throw err
    await backd.auth.signup({ email, password: PASSWORD })
  }
  return backd
}

const ada = await signedIn('ada@example.com')
const bob = await signedIn('bob@example.com')
const staff = await signedIn('staff@workshop.example')
const operator = await signedIn('operator@workshop.example')
const main = (/** @type {any} */ c) => c.db('main')

heading('The demo accounts')
check('staff has the staff role', (await staff.auth.me()).roles.includes('staff'))
check('the operator has the admin role', (await operator.auth.me()).roles.includes('admin'))
check('a customer has no role', (await ada.auth.me()).roles.length === 0)

heading('1. A function that reads as the caller (order_total)')
const order = await main(ada).collection('orders').create({ item: 'Anvil', quantity: 2, amount: 1250, status: 'draft' })
await refused('a customer can only create drafts (status "paid" refused)', () => main(ada).collection('orders').create({ item: 'x', quantity: 1, amount: 100, status: 'paid' }), ForbiddenError, '')
const total = /** @type {any} */ (await main(ada).fn('order_total', { order_id: order.id }))
check('the server adds the 20% tax', total.subtotal === 1250 && total.tax === 250 && total.total === 1500, JSON.stringify(total))
await refused("another customer's order doesn't exist for you", () => main(bob).fn('order_total', { order_id: order.id }), NotFoundError, 'not_found')

heading('3. A webhook from a payment provider (payment_webhook)')
const known = (await operator.admin.secrets.list()).some((s) => s.name === 'PAYMENT_WEBHOOK_SECRET' && s.database === 'main')
if (!known) await operator.admin.secrets.set('PAYMENT_WEBHOOK_SECRET', SECRET, { database: 'main' })
check('the operator can list the secret and never sees a value', (await operator.admin.secrets.list()).every((s) => !('value' in s)))
async function webhook(/** @type {string} */ eventId, /** @type {string} */ orderId, secret = SECRET) {
  const body = JSON.stringify({ id: eventId, type: 'payment.succeeded', order_id: orderId })
  const signature = `sha256=${createHmac('sha256', secret).update(body).digest('hex')}`
  const res = await fetch(`${url}/v1/workshop/main/_func/payment_webhook`, { method: 'POST', headers: { 'x-signature': signature }, body })
  return { status: res.status, text: (await res.text()).trim() }
}
const event = `evt_${Date.now().toString(36)}`
// The function reads its secret within about a minute of it being set.
let first = await webhook(event, order.id)
for (let i = 0; i < 70 && first.status === 500; i++) {
  await new Promise((r) => setTimeout(r, 1000))
  first = await webhook(event, order.id)
}
check('a signed event marks the order paid (200 ok)', first.status === 200 && first.text === 'ok', JSON.stringify(first))
const again = await webhook(event, order.id)
check('the same event again is ignored (200 already processed)', again.status === 200 && again.text === 'already processed', JSON.stringify(again))
const wrong = await webhook(`${event}-x`, order.id, 'not-the-secret')
check('a wrong signature is refused (400 invalid signature)', wrong.status === 400 && wrong.text === 'invalid signature', JSON.stringify(wrong))
const unknown = await webhook(`${event}-y`, 'no-such-order')
check('an event for an unknown order is a 404', unknown.status === 404, JSON.stringify(unknown))
check('the customer sees the order as paid', (await main(ada).collection('orders').get(order.id)).status === 'paid')

heading('2. A privileged, idempotent refund (refund, refund_receipt)')
const key = `refund-${order.id}`
await refused('a customer is refused (the invoke rule lets only staff in)', () => main(ada).fn('refund', { order_id: order.id }, { idempotencyKey: key }), ForbiddenError, '')
const refund = /** @type {any} */ (await main(staff).fn('refund', { order_id: order.id, reason: 'damaged' }, { idempotencyKey: key }))
check('staff refund the order', refund.amount === 1250 && typeof refund.refund_id === 'string' && typeof refund.receipt_job === 'string', JSON.stringify(refund))
const replay = /** @type {any} */ (await main(staff).fn('refund', { order_id: order.id, reason: 'damaged' }, { idempotencyKey: key }))
check('the same key returns the same refund', replay.refund_id === refund.refund_id && replay.receipt_job === refund.receipt_job)
await refused('a new key is another refund, and the order is already refunded', () => main(staff).fn('refund', { order_id: order.id }, { idempotencyKey: `${key}-again` }), Error, 'not_refundable')
check('the order is refunded', (await main(ada).collection('orders').get(order.id)).status === 'refunded')
// The receipt is a job queued by an internal function call.
let receiptJob = (await staff.request({ method: 'GET', path: ['main', '_jobs', refund.receipt_job] })).data
for (let i = 0; i < 40 && receiptJob.status !== 'done'; i++) {
  await new Promise((r) => setTimeout(r, 500))
  receiptJob = (await staff.request({ method: 'GET', path: ['main', '_jobs', refund.receipt_job] })).data
}
check('the receipt job finished ok', receiptJob.status === 'done' && receiptJob.result?.status === 'ok', JSON.stringify(receiptJob))
const receipts = await main(staff).collection('receipts').list({ where: { refund_id: refund.refund_id }, limit: 1 })
check('the receipt was written', receipts.items[0]?.text === `Refund of 12.50 for order ${order.id}`, JSON.stringify(receipts.items[0]))
for (const [who, c] of /** @type {[string, any][]} */ ([['staff', staff], ['anonymous', createClient({ url, realm: 'workshop' })]])) {
  await refused(`refund_receipt has no HTTP route (${who})`, () => main(c).fn('refund_receipt', {}), NotFoundError, 'not_found')
}

heading('4. A report in the background (export_orders)')
const job = /** @type {any} */ (await main(ada).fn('export_orders', {}))
check('the call answers with a job', typeof job.id === 'string' && ['queued', 'running', 'done'].includes(await job.status()))
const report = /** @type {any} */ (await job.wait({ pollIntervalMs: 300, timeoutMs: 60000 }))
const csv = (await main(ada).collection('reports').get(report.report_id)).csv
check("the report holds the customer's orders", report.rows >= 1 && csv.includes(order.id) && csv.startsWith('id,item,quantity,amount,status'), csv)
await refused("another customer can't read the report", () => main(bob).collection('reports').get(report.report_id), NotFoundError, 'not_found')

heading('5. Operating it (as the operator)')
const manual = /** @type {any} */ (await operator.admin.invokeFunction('main/nightly_cleanup'))
const cleaned = /** @type {any} */ (await manual.wait({ pollIntervalMs: 300, timeoutMs: 60000 }))
check('nightly_cleanup runs by hand through the admin API', typeof cleaned.deleted === 'number' && typeof cleaned.cutoff === 'string', JSON.stringify(cleaned))
await refused('nightly_cleanup has no HTTP route', () => main(operator).fn('nightly_cleanup', {}), NotFoundError, 'not_found')
const jobs = await operator.admin.jobs.list({ limit: 30 })
check('the job list shows attempts and the next attempt', jobs.items.length > 0 && jobs.items.every((j) => typeof j.attempts === 'number' && 'next_attempt_at' in j))
check('the job list has the refund receipt', jobs.items.some((j) => j.function === 'main/refund_receipt'))
const history = await operator.admin.invocations.list({ function: 'main/refund', limit: 20 })
const refundCall = history.items.find((r) => r.status === 'ok' && r.origin === 'http')
check('the history has the refund call, from http', refundCall !== undefined)
const request = await operator.admin.invocations.list({ requestId: refundCall?.request_id ?? 'none', limit: 20 })
const child = request.items.find((r) => r.function === 'main/refund_receipt')
check('its receipt was called by the refund (origin function, parent_id)', child?.origin === 'function' && child.parent_id === refundCall?.id, JSON.stringify(child))

console.log(failed === 0 ? '\nThe whole tour works.' : `\n${failed} check(s) failed.`)
process.exit(failed === 0 ? 0 : 1)
