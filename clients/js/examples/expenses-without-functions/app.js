// Expenses without functions: groups share expenses, and every member
// sees who owes whom. Alpine.js + backd-js, no build step.
//
// Everything the server enforces lives in examples/config/expenses: read
// its rules.yaml files. What they can't enforce is done here, in the
// browser, where any user can bypass it: see the docs page "Expenses
// without functions" and hack.js next to this file.
import { createClient, localStorageStorage, VerificationRequiredError, VersionMismatchError, ForbiddenError } from 'backd-js'
import { balances, settlementPlan, toCents } from './ledger.js'

const backd = createClient({
  url: window.location.origin, // same origin as the API (nginx), no CORS needed
  realm: 'expenses',
  storage: localStorageStorage('backd-expenses-example'),
})
const db = backd.db('main')
const groups = db.collection('groups')
const expenses = db.collection('expenses')

document.addEventListener('alpine:init', () => {
  window.Alpine.data('expensesApp', () => ({
    user: null,
    mode: 'login',
    email: '',
    password: '',
    groups: [],
    group: null, // the open group
    entries: [], // its expenses and settlements, newest first
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

    // Groups: the read rule only returns those listing the user's email.
    // Opens the first one when none is open yet.
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
      await this.loadEntries()
      await this.refreshMyCopies()
    },

    select(g) {
      return this.run(() => this.open(g))
    },

    async loadEntries() {
      this.entries = await all(expenses.iterate({ where: { group_id: this.group.id }, orderBy: '-_meta.created_at' }))
    },

    // Invitations are emails added to the group: the invited person sees it
    // when they sign up or log in with that email. (Only after verifying the
    // address: the realm gives no session before.)
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
      // New members join the default split; removed ones leave it.
      this.draft.split = [...this.group.members]
      await this.refreshMyCopies()
      await this.loadGroups()
    },

    // Each expense carries a copy of the group's members, because rules
    // can't read the group. Only an expense's writer may update it, so each
    // client refreshes its own expenses' copies when it sees the group
    // changed. (Hole 2: until the writer's app does this, removed members
    // still read old expenses and new members don't see them.)
    async refreshMyCopies() {
      const current = [...this.group.members].sort()
      for (const e of this.entries) {
        if (e._meta.owner !== this.user.id || sameSet(e.members, current)) continue
        try {
          const updated = await expenses.patch(e.id, { members: current }, { ifMatch: e._meta.version })
          this.entries = this.entries.map((x) => (x.id === e.id ? updated : x))
        } catch (err) {
          // Splits with a removed member can't be refreshed: the rules want
          // everyone in split_between to be in members.
          if (!(err instanceof ForbiddenError || err instanceof VersionMismatchError)) throw err
        }
      }
    },

    addExpense() {
      return this.run(async () => {
        const amount = toCents(this.draft.amount)
        if (!amount) throw new Error('Enter an amount above zero.')
        if (this.draft.split.length === 0) throw new Error('Split it with at least one person.')
        const e = await expenses.create({
          kind: 'expense',
          group_id: this.group.id,
          description: this.draft.description,
          amount,
          paid_by: this.user.email, // the rules only accept expenses you paid
          split_between: this.draft.split,
          members: [...this.group.members].sort(),
        })
        this.entries.unshift(e)
        this.draft = emptyDraft(this.group)
      })
    },

    // Record that the user paid back what the plan says they owe.
    // (Hole 3: the receiver never confirms it.)
    settle(to, amount) {
      if (!confirm(`Record that you paid ${to} ${this.money(amount)}?`)) return
      return this.run(async () => {
        const e = await expenses.create({
          kind: 'settlement',
          group_id: this.group.id,
          description: 'Settlement',
          amount,
          paid_by: this.user.email,
          split_between: [to],
          members: [...this.group.members].sort(),
        })
        this.entries.unshift(e)
      })
    },

    remove(e) {
      if (!confirm(`Delete "${e.description || e.kind}"?`)) return
      return this.run(async () => {
        await expenses.delete(e.id)
        this.entries = this.entries.filter((x) => x.id !== e.id)
      })
    },

    // Computed in the browser from the entries. (Hole 4: every client
    // computes its own; fake entries change everyone's numbers.)
    get balances() {
      return balances(this.entries)
    },
    get plan() {
      return settlementPlan(this.balances)
    },
    get people() {
      return [...new Set([...this.group.members, ...Object.keys(this.balances)])].sort()
    },

    // An entry paid by someone who isn't in the group: it can only come
    // from a former member, or from anyone who knew the group's id (hole 1).
    suspicious(e) {
      return !this.group.members.includes(e.paid_by)
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

function sameSet(a, b) {
  return a.length === b.length && [...a].sort().every((x, i) => x === b[i])
}

function describe(err) {
  const fields = (err.details ?? []).map((d) => `${d.path} ${d.reason}`)
  return [err.message, ...fields].join(' · ')
}
