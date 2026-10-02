// The workshop tour: a guided walk through what the workshop realm's
// functions do (docs: Functions -> Cookbook). One page, Alpine.js and
// @backd/client, no build step. Every call the app makes shows up in the
// inspector on the right, with the request, the answer, the function's own
// log lines and a curl command that repeats it.
//
// The realm's configuration is examples/config/workshop; the demo accounts
// below get their roles from its realm.yaml. They exist only in the local
// stack, with a fixed password: never do this in a real realm.
import { createClient, localStorageStorage } from '@backd/client'
import { createInspector } from './lib/inspector.js'
import { exportPanel } from './panels/export.js'
import { operatorPanel } from './panels/operator.js'
import { ordersPanel } from './panels/orders.js'
import { refundPanel } from './panels/refund.js'
import { verifyPanel } from './panels/verify.js'
import { webhookPanel } from './panels/webhook.js'

const REALM = 'workshop'
const PASSWORD = 'dev-p4ssw0rd!'

const ACCOUNTS = [
  { key: 'ada', name: 'Ada', role: 'customer', email: 'ada@example.com' },
  { key: 'bob', name: 'Bob', role: 'customer', email: 'bob@example.com' },
  { key: 'staff', name: 'Staff', role: 'staff', email: 'staff@workshop.example' },
  { key: 'operator', name: 'Operator', role: 'operator (admin)', email: 'operator@workshop.example' },
]

// The tour, in order.
const TOUR = [
  { n: 1, id: 'orders', title: 'A function that reads as the caller', fn: 'order_total', kind: 'sync' },
  { n: 2, id: 'refund', title: 'A privileged, idempotent refund', fn: 'refund', kind: 'sync, admin, calls an internal function' },
  { n: 3, id: 'webhook', title: 'A webhook from a payment provider', fn: 'payment_webhook', kind: 'webhook, signed, deduplicated' },
  { n: 4, id: 'export', title: 'A report in the background', fn: 'export_orders', kind: 'async job' },
  { n: 5, id: 'operating', title: 'Operating it', fn: 'nightly_cleanup, daily_digest', kind: 'cron, history, secrets' },
  { n: 6, id: 'email', title: 'Email verification', fn: 'email-capture', kind: 'delivery through your own function' },
]

let rerender = () => {}
const inspector = createInspector({ onChange: () => rerender() })

const backd = createClient({
  // The page and the API share one origin (nginx in docker-compose.yml).
  url: window.location.origin,
  realm: REALM,
  fetch: inspector.fetch,
  storage: localStorageStorage('backd-workshop-tour'),
})

const db = backd.db('main')

// Orders the page has seen, by any demo account: staff can't list customers'
// orders (the rules forbid it), so the refund panel offers the ones seen here.
const KNOWN_KEY = 'backd-workshop-tour-orders'
function loadKnown() {
  try {
    return JSON.parse(localStorage.getItem(KNOWN_KEY) ?? '[]')
  } catch {
    return []
  }
}

// Demo accounts this browser already created. The page signs a new one up and
// logs in to one it created: no call that is expected to fail, except when the
// stack was reset since (the account is gone) or the account was made
// elsewhere (the tour script), which the inspector then labels as expected.
const CREATED_KEY = 'backd-workshop-tour-accounts'
function loadCreated() {
  try {
    return JSON.parse(localStorage.getItem(CREATED_KEY) ?? '[]')
  } catch {
    return []
  }
}
function markCreated(email) {
  try {
    localStorage.setItem(CREATED_KEY, JSON.stringify([...new Set([...loadCreated(), email])]))
  } catch {
    // Not remembered: the next sign-in finds out again.
  }
}

/** Starts a session as a demo account, creating the account the first time. */
async function enter(account) {
  const credentials = { email: account.email, password: PASSWORD }
  if (!loadCreated().includes(account.email)) {
    try {
      inspector.expect(409, 'This account already exists (made outside this page): logging in instead.')
      await backd.auth.signup(credentials)
      markCreated(account.email)
      return
    } catch (err) {
      if (err.status !== 409) throw err
      markCreated(account.email)
    }
  }
  try {
    inspector.expect(401, 'This account is gone (the stack was reset): signing it up again.')
    await backd.auth.login(credentials)
  } catch (err) {
    if (err.status !== 401) throw err
    await backd.auth.signup(credentials)
  }
}

const money = (cents) => `€${(cents / 100).toFixed(2)}`

function describe(err) {
  if (err?.code) return `${err.status} ${err.code}: ${err.message}`
  return err?.message ?? String(err)
}

document.addEventListener('alpine:init', () => {
  window.Alpine.data('tour', () => ({
    ...ordersPanel({ db, ACCOUNTS, money }),
    ...refundPanel({ db, backd }),
    ...webhookPanel({ backd, fetchRaw: inspector.fetch, ACCOUNTS }),
    ...exportPanel({ db, backd }),
    ...operatorPanel({ backd }),
    ...verifyPanel({ backd, fetch: inspector.fetch, realm: REALM }),
    known: loadKnown(),
    accounts: ACCOUNTS,
    tour: TOUR,
    user: null,
    account: null, // the demo account signed in
    error: '',
    busy: false,
    entries: [],
    open: null, // the inspector entry whose details are open
    copied: null,

    async init() {
      rerender = () => {
        // Copies, so the page sees the entries change as calls finish.
        this.entries = inspector.entries.map((e) => ({ ...e }))
      }
      backd.auth.onAuthChange((event) => {
        if (event === 'SESSION_EXPIRED') {
          this.user = null
          this.account = null
          this.error = 'Your session ended. Sign in again.'
        }
      })
      if (await backd.auth.token()) {
        try {
          inspector.step('Resume the stored session')
          this.user = await backd.auth.me()
          this.account = ACCOUNTS.find((a) => a.email === this.user.email) ?? null
        } catch {
          // The stored session expired; onAuthChange handles it.
        }
      }
    },

    // Runs a step of the tour: labels the calls it makes in the inspector,
    // and shows an error instead of throwing.
    async step(label, action) {
      this.error = ''
      this.busy = true
      inspector.step(label)
      try {
        return await action()
      } catch (err) {
        this.error = describe(err)
      } finally {
        this.busy = false
      }
    },

    // Signs in as a demo account, creating it the first time: sign-up is open
    // in this realm, and the realm's seed assignments give staff and operator
    // their roles when their emails sign up.
    signInAs(account) {
      return this.step(`Sign in as ${account.name}`, () => this._signIn(account))
    },

    async _signIn(account) {
      if (this.user) await backd.auth.logout().catch(() => {})
      this.user = null
      this.account = null
      await enter(account)
      this.user = await backd.auth.me()
      this.account = account
      this.orders = []
      this.totals = {}
      this.exportRuns = []
      this.chain = null
      if (account.role === 'customer') await this.loadOrders()
      if (account.key === 'operator') await this.refreshOps()
    },

    // Remembers the orders just seen, for the refund panel.
    remember(orders) {
      const byId = new Map(this.known.map((o) => [o.id, o]))
      for (const o of orders) byId.set(o.id, { id: o.id, item: o.item, amount: o.amount, status: o.status, owner: this.account?.name ?? '?' })
      this.known = [...byId.values()]
      try {
        localStorage.setItem(KNOWN_KEY, JSON.stringify(this.known))
      } catch {
        // Not saved: the list lasts until the page closes.
      }
    },

    get isCustomer() {
      return this.account?.role === 'customer'
    },
    get isOperator() {
      return this.account?.key === 'operator'
    },
    get isStaff() {
      return this.account?.key === 'staff'
    },

    signOut() {
      return this.step('Sign out', async () => {
        await backd.auth.logout()
        this.user = null
        this.account = null
      })
    },

    whoAmI() {
      return this.step('Who am I?', async () => {
        this.user = await backd.auth.me()
      })
    },

    toggle(entry) {
      this.open = this.open === entry.id ? null : entry.id
    },

    async copyCurl(entry) {
      try {
        await navigator.clipboard.writeText(entry.curl)
        this.copied = entry.id
        setTimeout(() => (this.copied = null), 1500)
      } catch {
        this.error = 'Copying needs a secure page (https://localhost:8443) and permission to use the clipboard.'
      }
    },

    clearInspector() {
      inspector.clear()
    },

    badge(entry) {
      if (entry.status === null) return entry.error ? 'error' : '…'
      return String(entry.status)
    },
    statusClass(entry) {
      if (entry.status === null) return entry.error ? 'bad' : 'wait'
      if (entry.note) return 'expected'
      return entry.status < 400 ? 'ok' : 'bad'
    },
  }))
})
