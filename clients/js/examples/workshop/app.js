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

const REALM = 'workshop'
const PASSWORD = 'dev-p4ssw0rd!'

const ACCOUNTS = [
  { key: 'ada', name: 'Ada', role: 'customer', email: 'ada@example.com' },
  { key: 'bob', name: 'Bob', role: 'customer', email: 'bob@example.com' },
  { key: 'staff', name: 'Staff', role: 'staff', email: 'staff@workshop.example' },
  { key: 'operator', name: 'Operator', role: 'operator (admin)', email: 'operator@workshop.example' },
]

// The tour, in order. The panels arrive step by step.
const TOUR = [
  { n: 1, title: 'A function that reads as the caller', fn: 'order_total', kind: 'sync' },
  { n: 2, title: 'A privileged, idempotent refund', fn: 'refund', kind: 'sync, admin, calls an internal function' },
  { n: 3, title: 'A webhook from a payment provider', fn: 'payment_webhook', kind: 'webhook, signed, deduplicated' },
  { n: 4, title: 'A report in the background', fn: 'export_orders', kind: 'async job' },
  { n: 5, title: 'Operating it', fn: 'nightly_cleanup, daily_digest', kind: 'cron, history, secrets' },
  { n: 6, title: 'Email', fn: 'email-capture', kind: 'delivery through your own function' },
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

function describe(err) {
  if (err?.code) return `${err.status} ${err.code}: ${err.message}`
  return err?.message ?? String(err)
}

document.addEventListener('alpine:init', () => {
  window.Alpine.data('tour', () => ({
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
      return this.step(`Sign in as ${account.name}`, async () => {
        if (this.user) await backd.auth.logout().catch(() => {})
        this.user = null
        this.account = null
        try {
          await backd.auth.login({ email: account.email, password: PASSWORD })
        } catch (err) {
          if (err.status !== 401) throw err
          await backd.auth.signup({ email: account.email, password: PASSWORD })
        }
        this.user = await backd.auth.me()
        this.account = account
      })
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
      return entry.status < 400 ? 'ok' : 'bad'
    },
  }))
})
