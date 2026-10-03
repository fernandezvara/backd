// Expenses with functions: the same app as expenses-without-functions,
// with holes 1-4 closed by server-side functions instead of client-trusted
// copies. Alpine.js + backd-js, no build step.
//
// Compare this file with ../expenses-without-functions/app.js: groups
// didn't change at all (no holes there); only how expenses are created,
// read, settled and totaled did. See the docs page "Expenses with
// functions" for what changed and why, and examples/config/
// expenses-with-functions for the rules and the functions themselves.
import { createClient, localStorageStorage, VerificationRequiredError, VersionMismatchError } from 'backd-js'
import { toCents } from './ledger.js'

const backd = createClient({
  url: window.location.origin, // same origin as the API (nginx), no CORS needed
  realm: 'expenses-with-functions',
  storage: localStorageStorage('backd-expenses-functions-example'),
})
const db = backd.db('main')
const groups = db.collection('groups')
// No `expenses` collection reads here: a direct read only ever returns
// your own entries (expenses/rules.yaml), so list_expenses is the only
// way to see everyone's. Deleting your own entry is still a direct,
// rule-checked document operation — see remove().
const expenses = db.collection('expenses')

document.addEventListener('alpine:init', () => {
  window.Alpine.data('expensesApp', () => ({
    user: null,
    mode: 'login',
    email: '',
    password: '',
    groups: [],
    group: null, // the open group
    entries: [], // its expenses and settlements, newest first (from list_expenses)
    balances: {}, // from the balances function, not computed here
    plan: [], // ditto
    newGroup: { name: '', currency: 'EUR' },
    invite: '',
    draft: emptyDraft(),
    error: '',
    notice: '', // what to do next after signing up
    busy: false,

    async init() {
      backd.auth.onAuthChange((event) => {
        if (event === 'SESSION_EXPIRED') {
          this.user = null
          this.group = null
          this.error = 'Your session ended. Please log in again.'
        }
      })
      if (await backd.auth.token()) {
        try {
          this.user = await backd.auth.me()
          await this.loadGroups()
        } catch {
          // Expired session: onAuthChange handles it.
        }
      }
    },

    async run(action) {
      this.error = ''
      this.busy = true
      try {
        await action()
      } catch (err) {
        this.error = describe(err)
      } finally {
        this.busy = false
      }
    },

    // "Forgot password?": backd answers the same for every address, so the
    // message doesn't say whether this one has an account. The link opens a
    // page of backd (it works with no app code) and sends the user back here.
    forgotPassword() {
      return this.run(async () => {
        await backd.auth.requestPasswordReset({ email: this.email })
        this.notice = `If ${this.email} has an account, we sent it a link to choose a new password.`
        this.mode = 'login'
      })
    },

    submitAuth() {
      return this.run(async () => {
        this.notice = ''
        const credentials = { email: this.email, password: this.password }
        let session
        try {
          session = this.mode === 'signup' ? await backd.auth.signup(credentials) : await backd.auth.login(credentials)
        } catch (err) {
          // The realm gives no session before the address is verified: the
          // account exists, and the link in the email finishes it.
          if (!(err instanceof VerificationRequiredError)) throw err
          this.notice = `We sent a link to ${this.email}. Follow it to verify your address, then log in.`
          this.mode = 'login'
          this.password = ''
          return
        }
        this.user = session.user
        this.password = ''
        await this.loadGroups()
      })
    },

    logout() {
      return this.run(async () => {
        await backd.auth.logout()
        this.user = null
        this.groups = []
        this.group = null
      })
    },

    // Groups: unchanged from expenses-without-functions — the read rule
    // only returns those listing the user's email. Opens the first one
    // when none is open yet.
    async loadGroups() {
      this.groups = await all(groups.iterate({ orderBy: 'name' }))
      if (!this.group && this.groups.length > 0) await this.open(this.groups[0])
    },

    createGroup() {
      return this.run(async () => {
        const g = await groups.create({ name: this.newGroup.name, currency: this.newGroup.currency.toUpperCase(), members: [this.user.email] })
        this.newGroup = { name: '', currency: 'EUR' }
        await this.loadGroups()
        await this.open(g)
      })
    },

    async open(g) {
      this.group = await groups.get(g.id)
      this.draft = emptyDraft(this.group)
      await this.refresh()
    },

    select(g) {
      return this.run(() => this.open(g))
    },

    // Reloads both the entries and the balances from the server: neither
    // is trustworthy computed any other way (holes 1, 2 and 4).
    async refresh() {
      const [{ items }, computed] = await Promise.all([
        db.fn('list_expenses', { group_id: this.group.id }),
        db.fn('balances', { group_id: this.group.id }),
      ])
      this.entries = items
      this.balances = computed.balances
      this.plan = computed.plan
    },

    // Invitations are emails added to the group: the invited person sees
    // it when they sign up or log in with that email. (Hole 5: until
    // emails are verified, whoever signs up first with that email gets
    // it — unchanged from expenses-without-functions; see the docs.)
    addMember() {
      return this.run(async () => {
        const email = this.invite.trim().toLowerCase()
        if (!email || this.group.members.includes(email)) return
        await this.changeMembers([...this.group.members, email])
        this.invite = ''
      })
    },

    removeMember(email) {
      if (!confirm(`Remove ${email} from ${this.group.name}?`)) return
      return this.run(() => this.changeMembers(this.group.members.filter((m) => m !== email)))
    },

    async changeMembers(members) {
      try {
        this.group = await groups.patch(this.group.id, { members }, { ifMatch: this.group._meta.version })
      } catch (err) {
        if (!(err instanceof VersionMismatchError)) throw err
        this.group = await groups.get(this.group.id)
        throw new Error('The group changed meanwhile; it was reloaded. Try again.')
      }
      this.draft.split = [...this.group.members]
      // No copies to refresh any more: list_expenses checks membership
      // fresh, so a removed member simply stops being able to list
      // anything for this group, and a new member sees everything at once.
      await this.refresh()
      await this.loadGroups()
    },

    addExpense() {
      return this.run(async () => {
        const amount = toCents(this.draft.amount)
        if (!amount) throw new Error('Enter an amount above zero.')
        if (this.draft.split.length === 0) throw new Error('Split it with at least one person.')
        // add_expense checks group_id and split_between against the real,
        // current group before writing anything (closes hole 1).
        await db.fn('add_expense', {
          group_id: this.group.id,
          description: this.draft.description,
          amount,
          split_between: this.draft.split,
        })
        this.draft = emptyDraft(this.group)
        await this.refresh()
      })
    },

    // Records that the user says they paid someone back. Unlike
    // expenses-without-functions, this alone doesn't move the balance:
    // it's "pending" until the receiver confirms it (hole 3).
    settle(to, amount) {
      if (!confirm(`Record that you paid ${to} ${this.money(amount)}? They'll need to confirm it.`)) return
      return this.run(async () => {
        await db.fn('request_settlement', { group_id: this.group.id, to, amount })
        await this.refresh()
      })
    },

    // The receiver of a pending settlement confirms it really happened —
    // the other half of closing hole 3. Only shown to them (see
    // pendingForMe below); confirm_settlement itself also checks it.
    confirmSettlement(entry) {
      return this.run(async () => {
        await db.fn('confirm_settlement', { id: entry.id })
        await this.refresh()
      })
    },

    // Deleting your own entry is still a direct, rule-checked document
    // operation (expenses/rules.yaml's delete rule, owner-only, unchanged
    // from expenses-without-functions) — no function needed for this one.
    remove(e) {
      if (!confirm(`Delete "${e.description || e.kind}"?`)) return
      return this.run(async () => {
        await expenses.delete(e.id)
        await this.refresh()
      })
    },

    get people() {
      return [...new Set([...this.group.members, ...Object.keys(this.balances)])].sort()
    },

    // A pending settlement this user is being asked to confirm.
    pendingForMe(e) {
      return e.kind === 'settlement' && e.status === 'pending' && e.split_between[0] === this.user.email
    },
    isMine(e) {
      return this.user !== null && e._meta.owner === this.user.id
    },
    isCreator() {
      return this.group && this.group._meta.owner === this.user.id
    },
    money(cents) {
      return new Intl.NumberFormat(undefined, { style: 'currency', currency: this.group.currency }).format(cents / 100)
    },
  }))
})

async function all(iterator) {
  const out = []
  for await (const x of iterator) out.push(x)
  return out
}

function emptyDraft(group) {
  return { description: '', amount: '', split: group ? [...group.members] : [] }
}

function describe(err) {
  const fields = (err.details ?? []).map((d) => `${d.path} ${d.reason}`)
  return [err.message, ...fields].join(' · ')
}
