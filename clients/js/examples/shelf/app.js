// Shelf — the tutorial app (docs: Tutorial).
//
// Chapter 5 adds the first functions: curators `publish` drafts, members
// mint `share` links, and `?s=<token>` resolves one publicly through
// `share-open` — the holes the rules chapter left, closed.
import { createClient, localStorageStorage, VersionMismatchError } from 'backd-js'

const backd = createClient({
  url: window.location.origin,
  realm: 'shelf',
  // Sessions survive reloads. In a real app, guard against XSS before
  // trusting localStorage (or use cookie sessions).
  storage: localStorageStorage('shelf'),
})
const main = backd.db('main')
const assets = main.collection('assets')
const shares = main.collection('shares')
const notifications = main.collection('notifications')
const members = main.collection('members')

document.addEventListener('alpine:init', () => {
  Alpine.data('shelf', () => ({
    // Session: null while signed out.
    user: null,
    mode: 'login', // login | signup
    email: '',
    password: '',
    invitation: '', // signup: invite — the token the operator hands over

    // Admin: invitations pending, and the token of the one just created.
    inviteEmail: '',
    inviteLink: null,
    pendingInvites: [],

    // My assets and the account page.
    mine: [],
    editing: null, // { id, version, title, url, body, tags } — the card being edited
    pwd: { current: '', next: '' },
    accountMsg: null,

    // Notifications: mine, newest first (functions write them; I can only
    // flip read_at — the rules say so).
    notifs: [],

    // Admin "run by hand": digest runs as an async job — the call answers
    // with a job, and its status is polled.
    fnName: 'digest',
    jobId: null,
    jobStatus: null,
    jobResult: null,

    // Shares: the asset being shared, the minted link, my active links, and
    // the public ?s=<token> view.
    sharing: null,
    shareExpiry: 'week',
    shareLink: null,
    myShares: [],
    sharedDoc: null,
    shareErr: null,

    // Gallery state: the page of published assets, the tag filter and the
    // cursor that continues the list.
    assets: [],
    after: undefined,
    hasMore: false,
    tag: '',
    sort: '-published_at',
    error: null,
    busy: false,

    // The "new asset" form (tags come in as one comma-separated string).
    draft: { title: '', kind: 'link', url: '', body: '', tags: '' },

    async init() {
      // An invitation link (?token=…) preselects the sign-up form.
      const params = new URLSearchParams(window.location.search)
      const token = params.get('token')
      if (token) {
        this.invitation = token
        this.mode = 'signup'
      }
      // ?s=<token> is the public share view — no session needed.
      const s = params.get('s')
      if (s) await this.openShare(s)
      try {
        this.user = await backd.auth.me()
      } catch {
        this.user = null
      }
      await this.refilter()
      if (this.user) await Promise.all([this.loadMine(), this.loadInvites(), this.loadShares(), this.loadNotifs(), this.ensureMember()])
    },

    // --- session -----------------------------------------------------------

    async submitAuth() {
      this.busy = true
      this.error = null
      try {
        const credentials = { email: this.email, password: this.password }
        if (this.mode === 'signup' && this.invitation) credentials.invitation = this.invitation
        const session = this.mode === 'signup'
          ? await backd.auth.signup(credentials)
          : await backd.auth.login(credentials)
        this.user = session.user
        this.password = ''
        this.invitation = ''
        await this.refilter()
        await this.loadMine()
        await this.loadInvites()
        await this.loadNotifs()
        await this.ensureMember()
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    async logout() {
      await backd.auth.logout().catch(() => {})
      this.user = null
      this.mine = []
      await this.refilter()
    },

    // `{"tags": "docs"}` finds documents whose tags array contains "docs".
    where() {
      return this.tag ? { tags: this.tag } : {}
    },

    async load() {
      if (!this.user) return
      this.busy = true
      this.error = null
      try {
        const page = await assets.list({
          where: this.where(),
          orderBy: this.sort,
          limit: 9,
          after: this.after,
        })
        this.assets = this.assets.concat(page.items)
        this.after = page.next_cursor
        this.hasMore = page.has_more
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    // A new filter or sort restarts the list from the first page.
    async refilter() {
      this.assets = []
      this.after = undefined
      await this.load()
    },

    async create() {
      this.busy = true
      this.error = null
      try {
        // Chapter 5: members save drafts; published_at belongs to publish().
        const doc = {
          title: this.draft.title,
          kind: this.draft.kind,
        }
        if (this.draft.url) doc.url = this.draft.url
        if (this.draft.body) doc.body = this.draft.body
        const tags = this.draft.tags.split(',').map((t) => t.trim()).filter(Boolean)
        if (tags.length) doc.tags = tags
        await assets.create(doc)
        this.draft = { title: '', kind: 'link', url: '', body: '', tags: '' }
        await this.refilter()
        await this.loadMine()
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    // --- my assets & account -------------------------------------------------

    async loadMine() {
      if (!this.user) return
      try {
        const page = await assets.list({
          where: { '_meta.owner': this.user.id },
          orderBy: '-published_at',
          limit: 50,
        })
        this.mine = page.items
      } catch (e) {
        this.error = e.message
      }
    },

    startEdit(asset) {
      this.editing = {
        id: asset.id,
        version: asset._meta?.version,
        title: asset.title,
        url: asset.url ?? '',
        body: asset.body ?? '',
        tags: (asset.tags ?? []).join(', '),
      }
    },

    async saveEdit() {
      if (!this.editing) return
      this.busy = true
      this.error = null
      try {
        const patch = { title: this.editing.title, body: this.editing.body }
        const tags = this.editing.tags.split(',').map((t) => t.trim()).filter(Boolean)
        patch.tags = tags
        if (this.editing.url) patch.url = this.editing.url
        // If-Match: the write is conditional on the version we read.
        await assets.patch(this.editing.id, patch, { ifMatch: this.editing.version })
        this.editing = null
        await this.refilter()
        await this.loadMine()
      } catch (e) {
        this.error = e instanceof VersionMismatchError
          ? 'Someone else changed this asset first — reload and try again.'
          : e.message
      } finally {
        this.busy = false
      }
    },

    get isAdmin() {
      return (this.user?.roles ?? []).includes('admin')
    },

    async loadInvites() {
      if (!this.isAdmin) return
      try {
        this.pendingInvites = await backd.admin.invitations.list()
      } catch {
        this.pendingInvites = []
      }
    },

    async invite() {
      this.busy = true
      this.error = null
      this.inviteLink = null
      try {
        const inv = await backd.admin.invitations.create({ email: this.inviteEmail || undefined })
        // The token exists only in this response — build the link to hand over.
        this.inviteLink = `${location.origin}/?token=${inv.token}`
        this.inviteEmail = ''
        await this.loadInvites()
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    async revokeInvite(id) {
      await backd.admin.invitations.revoke(id).catch((e) => { this.error = e.message })
      await this.loadInvites()
    },

    async remove(asset) {
      if (!confirm(`Delete "${asset.title}"?`)) return
      this.busy = true
      this.error = null
      try {
        await assets.delete(asset.id, { ifMatch: asset._meta?.version })
        await this.refilter()
        await this.loadMine()
      } catch (e) {
        this.error = e instanceof VersionMismatchError
          ? 'Someone else changed this asset first — reload and try again.'
          : e.message
      } finally {
        this.busy = false
      }
    },

    // --- functions (chapter 5) -----------------------------------------------

    get isCurator() {
      return (this.user?.roles ?? []).includes('curator')
    },

    async publish(asset) {
      this.busy = true
      this.error = null
      try {
        // The key makes a double-click (or a retry) answer the first call's
        // result instead of publishing twice.
        await main.fn('publish', { asset_id: asset.id }, { idempotencyKey: `publish-${asset.id}` })
        await this.refilter()
        await this.loadMine()
        await this.loadNotifs()
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    startShare(asset) {
      this.sharing = asset
      this.shareLink = null
      location.hash = 'share'
    },

    async createShare() {
      if (!this.sharing) return
      this.busy = true
      this.error = null
      try {
        const out = await main.fn('share', { asset_id: this.sharing.id, expires_in: this.shareExpiry },
          { idempotencyKey: crypto.randomUUID() })
        this.shareLink = `${location.origin}/?s=${out.token}`
        await this.loadShares()
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    async loadShares() {
      if (!this.user) return
      try {
        const page = await shares.list({ where: { created_by: this.user.id }, orderBy: '-_meta.created_at', limit: 50 })
        this.myShares = page.items
      } catch (e) {
        this.error = e.message
      }
    },

    async revokeShare(share) {
      await shares.delete(share.id).catch((e) => { this.error = e.message })
      await this.loadShares()
    },

    async openShare(token) {
      try {
        this.sharedDoc = await main.fn('share-open', { token })
      } catch (e) {
        this.shareErr = e.message
      }
    },

    async fetchPreview() {
      if (!this.draft.url) return
      this.busy = true
      this.error = null
      try {
        const p = await main.fn('preview', { url: this.draft.url })
        if (p.title && !this.draft.title) this.draft.title = p.title
        if (p.description && !this.draft.body) this.draft.body = p.description
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    get unreadCount() {
      return this.notifs.filter((n) => !n.read_at).length
    },

    async loadNotifs() {
      if (!this.user) return
      try {
        const page = await notifications.list({
          where: { to_user: this.user.id },
          orderBy: '-_meta.created_at',
          limit: 50,
        })
        this.notifs = page.items
      } catch (e) {
        this.error = e.message
      }
    },

    async markAllRead() {
      const now = new Date().toISOString()
      for (const n of this.notifs.filter((n) => !n.read_at)) {
        await notifications.patch(n.id, { read_at: now }).catch((e) => { this.error = e.message })
      }
      await this.loadNotifs()
    },

    // The digest needs a member list; users live in the system database a
    // function can't read — so the app upserts a member doc on sign-in.
    async ensureMember() {
      if (!this.user) return
      try {
        const page = await members.list({ where: { user_id: this.user.id }, limit: 1 })
        if (page.items.length === 0) {
          await members.create({ user_id: this.user.id, email: this.user.email })
        } else if (page.items[0].email !== this.user.email) {
          await members.patch(page.items[0].id, { email: this.user.email })
        }
      } catch (e) {
        this.error = e.message
      }
    },

    async runFunction() {
      this.busy = true
      this.error = null
      this.jobId = this.jobStatus = this.jobResult = null
      try {
        const job = await main.fn(this.fnName, this.fnName === 'digest' ? { since_days: 7 } : {})
        this.jobId = job.id
        while ((this.jobStatus = await job.status()) !== 'done') {
          await new Promise((r) => setTimeout(r, 500))
        }
        this.jobResult = job.data?.result
        await this.loadNotifs()
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    async changePassword() {
      this.busy = true
      this.accountMsg = null
      try {
        await backd.auth.changePassword({
          currentPassword: this.pwd.current,
          newPassword: this.pwd.next,
        })
        this.pwd = { current: '', next: '' }
        this.accountMsg = 'Password changed.'
      } catch (e) {
        this.accountMsg = e.message
      } finally {
        this.busy = false
      }
    },

    async deleteAccount() {
      if (!confirm('Deactivate your account? Your assets follow the collection\'s erasure policy.')) return
      const password = prompt('Confirm with your password:')
      if (!password) return
      this.busy = true
      try {
        await backd.auth.deleteAccount({ password })
        await this.logout()
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },
  }))
})
