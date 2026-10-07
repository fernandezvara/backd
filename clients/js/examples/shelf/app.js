// Shelf — the tutorial app (docs: Tutorial).
//
// Chapter 5 adds the first functions: curators `publish` drafts, members
// mint `share` links, and `?s=<token>` resolves one publicly through
// `share-open` — the holes the rules chapter left, closed.
//
// Chapters 13–15 add files: an asset can carry one file (`file`, through backd)
// and up to five attachments (straight to the bucket, with a progress bar), the
// thumbnail function makes a picture of an image, downloads are counted by the
// `download` function, and a shared asset comes with links to its files.
import { createClient, localStorageStorage, NotFoundError, VersionMismatchError } from 'backd-js'

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
const outbox = backd.db('mail').collection('outbox')

const VIEWS = ['gallery', 'mine', 'review', 'notifications', 'admin', 'auth', 'account']

document.addEventListener('alpine:init', () => {
  Alpine.data('shelf', () => ({
    // Session: null while signed out.
    user: null,
    // A realm with `auth: disabled` (chapter 1) has no accounts: the gallery
    // is open to everyone, and the other views stay empty.
    open: false,
    // The one section showing; the nav's #links move between them.
    view: 'gallery',
    mode: 'login', // login | signup
    email: '',
    password: '',
    invitation: '', // signup: invite — the token the operator hands over

    // Admin: invitations pending, and the token of the one just created.
    inviteEmail: '',
    inviteSend: true, // chapter 10: email the invitation, or show the link
    inviteLink: null,
    pendingInvites: [],
    mailbox: [],
    newEmail: '',
    audit: [],
    apiKeys: [],
    newKeyName: '',
    newKey: null,

    // My assets and the account page.
    mine: [],
    // Curators: every member's unpublished assets, oldest first (chapter 5).
    drafts: [],
    // user id -> email, from the members directory (chapter 8): lets a card
    // say who published it. Empty until that chapter exists.
    names: {},
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
    // `file` and `attachments` hold what the pickers chose (chapters 13, 14).
    draft: { title: '', kind: 'link', url: '', body: '', tags: '', file: null, attachments: [] },
    // The upload in progress, for the bar: { label, pct }.
    progress: null,
    // Admin: what the storage holds (chapter 15).
    storage: null,

    async init() {
      window.addEventListener('hashchange', () => this.syncView())
      // An invitation link (?token=…) preselects the sign-up form.
      const params = new URLSearchParams(window.location.search)
      const token = params.get('token')
      if (token) {
        this.invitation = token
        this.mode = 'signup'
        window.location.hash = '#auth'
      }
      // ?s=<token> is the public share view — no session needed.
      const s = params.get('s')
      if (s) await this.openShare(s)
      try {
        this.user = await backd.auth.me()
      } catch (e) {
        this.user = null
        this.open = e instanceof NotFoundError
      }
      await this.refilter()
      if (this.user) await Promise.all([this.loadMine(), this.loadDrafts(), this.loadInvites(), this.loadShares(), this.loadNotifs(), this.ensureMember(), this.loadAdmin()])
      if (this.user) await this.loadNames()
      this.syncView()
    },

    syncView() {
      let v = window.location.hash.slice(1)
      if (!VIEWS.includes(v)) v = 'gallery'
      if (v === 'auth' && this.user) v = 'gallery'
      if (v === 'account' && !this.user) v = 'gallery'
      this.view = v
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
        await this.loadDrafts()
        await this.loadInvites()
        await this.loadNotifs()
        await this.ensureMember()
        await this.loadNames()
        await this.loadAdmin()
        window.location.hash = '#mine'
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
      this.drafts = []
      window.location.hash = '#gallery'
      await this.refilter()
    },

    // `{"tags": "docs"}` finds documents whose tags array contains "docs".
    // The gallery shows what is published — the rules also let a member see
    // their own drafts and a curator everyone's, so the filter is ours to add.
    where() {
      const where = { published_at: { $ne: null } }
      if (this.tag) where.tags = this.tag
      return where
    },

    async load() {
      if (!this.user && !this.open) return
      this.busy = true
      this.error = null
      try {
        const page = await assets.list({
          where: this.where(),
          orderBy: this.sort,
          limit: 9,
          after: this.after,
          // Chapter 13: every file comes with a link, for the previews shown at
          // once. They expire (five minutes), so downloads fetch a fresh one.
          fileLinks: true,
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

    // --- files (chapters 13-15) ------------------------------------------------

    // The bar: a small helper the uploads below share.
    reportProgress(label) {
      return ({ loaded, total }) => { this.progress = { label, pct: total ? Math.round((100 * loaded) / total) : 0 } }
    },

    pickFile(event) {
      this.draft.file = event.target.files[0] ?? null
    },

    pickAttachments(event) {
      this.draft.attachments = [...event.target.files]
    },

    prettySize(bytes) {
      if (bytes < 1024) return `${bytes} B`
      if (bytes < 1024 ** 2) return `${(bytes / 1024).toFixed(1)} KiB`
      if (bytes < 1024 ** 3) return `${(bytes / 1024 ** 2).toFixed(1)} MiB`
      return `${(bytes / 1024 ** 3).toFixed(1)} GiB`
    },

    isImage(file) {
      return Boolean(file?.type?.startsWith('image/'))
    },

    // The picture an asset's card shows: the thumbnail once the function made
    // it, else the image itself — both links came with the list (`fileLinks`).
    previewUrl(asset) {
      return asset.thumbnail?.url ?? (this.isImage(asset.file) ? asset.file.url : null)
    },

    // The files an asset holds, as a flat list for the download buttons.
    filesOf(asset) {
      const out = []
      if (asset.file) out.push({ field: 'file', ...asset.file })
      for (const f of asset.attachments ?? []) out.push({ field: 'attachments', ...f })
      return out
    },

    // Chapter 15: a download goes through the `download` function, which counts
    // it and answers a fresh link for the browser to open. The link makes the
    // browser save the file and leave the page where it is.
    async download(asset, file) {
      this.error = null
      try {
        const { url } = await main.fn('download', { document_id: asset.id, field: file.field, file_id: file.id })
        window.location.assign(url)
        setTimeout(() => this.refilter(), 1500) // the count moved
      } catch (e) {
        this.error = e.message
      }
    },

    // Chapter 13: the owner's own downloads need no counting: the client makes
    // the link when the button is clicked (a link fetched earlier may have expired).
    async downloadMine(asset, file) {
      this.error = null
      try {
        await assets.downloadFile(asset.id, file.field, file.id)
      } catch (e) {
        this.error = e.message
      }
    },

    // Chapter 14: the thumbnail is a job; the app doesn't wait for it, it
    // refreshes the lists when the job is done.
    async makeThumbnail(assetId) {
      try {
        const job = await main.fn('thumbnail', { asset_id: assetId })
        job.wait({ pollIntervalMs: 500, timeoutMs: 60000 }).then(() => Promise.all([this.loadMine(), this.refilter()])).catch(() => {})
      } catch {
        // a thumbnail is a nicety: the asset is saved either way
      }
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
        // Chapters 13-14: the files go first, as pending uploads (the asset
        // doesn't exist yet), and the create names them: `{ upload, token }`.
        // The client sends `file` through backd and `attachments` straight to the bucket.
        if (this.draft.kind === 'file' && this.draft.file) {
          const up = await assets.prepareUpload('file', this.draft.file, { onProgress: this.reportProgress(this.draft.file.name) })
          doc.file = up.ref
        }
        if (this.draft.kind === 'file' && this.draft.attachments.length) {
          doc.attachments = []
          for (const f of this.draft.attachments) {
            const up = await assets.prepareUpload('attachments', f, { onProgress: this.reportProgress(f.name) })
            doc.attachments.push(up.ref)
          }
        }
        const made = await assets.create(doc)
        if (this.isImage(made.file)) this.makeThumbnail(made.id)
        this.draft = { title: '', kind: 'link', url: '', body: '', tags: '', file: null, attachments: [] }
        for (const input of document.querySelectorAll('input[type=file]')) input.value = ''
        await this.refilter()
        await this.loadMine()
      } catch (e) {
        // 413: over the field's max_size; 415: not a type it takes (backd looks
        // at the bytes, not the name); 409 too_many_files: past max_files.
        this.error = e.message
      } finally {
        this.progress = null
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
          fileLinks: true,
        })
        this.mine = page.items
      } catch (e) {
        this.error = e.message
      }
    },

    // A curator's review queue: unpublished assets of every member (the
    // assets `read` rule allows it for the curator role), oldest first.
    async loadDrafts() {
      if (!this.user || !this.isCurator) {
        this.drafts = []
        return
      }
      try {
        const page = await assets.list({ where: { published_at: null }, orderBy: '_meta.created_at', limit: 50 })
        this.drafts = page.items
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

    // Replace the file of an asset, or add an attachment (chapters 13-14). Both
    // are updates of the asset: the update rule is asked before a byte moves,
    // and `ifMatch` makes them lose to a concurrent change instead of winning it.
    async putFile(asset, field, event) {
      const picked = event.target.files[0]
      event.target.value = ''
      if (!picked) return
      this.busy = true
      this.error = null
      try {
        const doc = await assets.uploadFile(asset.id, field, picked, { ifMatch: asset._meta.version, onProgress: this.reportProgress(picked.name) })
        if (field === 'file' && this.isImage(doc.file)) this.makeThumbnail(asset.id)
        await this.loadMine()
        await this.refilter()
      } catch (e) {
        this.error = e instanceof VersionMismatchError ? 'Someone else changed this asset first — reload and try again.' : e.message
      } finally {
        this.progress = null
        this.busy = false
      }
    },

    async removeFile(asset, file) {
      if (!confirm(`Remove ${file.name}?`)) return
      this.busy = true
      this.error = null
      try {
        await assets.deleteFile(asset.id, file.field, file.id, { ifMatch: asset._meta.version })
        await this.loadMine()
        await this.refilter()
      } catch (e) {
        this.error = e instanceof VersionMismatchError ? 'Someone else changed this asset first — reload and try again.' : e.message
      } finally {
        this.busy = false
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

    async createInvite() {
      this.busy = true
      this.error = null
      this.inviteLink = null
      try {
        if (this.inviteSend && this.inviteEmail) {
          // Chapter 10: backd emails the invitation — the mail lands in the
          // dev outbox below.
          await backd.admin.invitations.send({ email: this.inviteEmail, redirectTo: location.origin + '/' })
          this.accountMsg = `Invitation emailed to ${this.inviteEmail} — check the mailbox.`
          await this.loadMailbox()
        } else {
          const inv = await backd.admin.invitations.create({ email: this.inviteEmail || undefined })
          this.inviteLink = `${location.origin}/?token=${inv.token}`
        }
        this.inviteEmail = ''
        await this.loadInvites()
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    async loadAdmin() {
      if (!this.isAdmin) return
      await Promise.all([this.loadMailbox(), this.loadAudit(), this.loadKeys(), this.loadStorage()])
    },

    // Chapter 15: what the storage holds, for the operator — how it is set up,
    // whether it answers, and the totals of what documents reference.
    async loadStorage() {
      if (!this.isAdmin) return
      try {
        this.storage = await backd.admin.storage.status()
      } catch (e) {
        this.error = e.message
      }
    },

    async loadAudit() {
      if (!this.isAdmin) return
      try {
        const page = await backd.admin.audit.list({ limit: 20 })
        this.audit = page.items
      } catch (e) {
        this.error = e.message
      }
    },

    async loadKeys() {
      if (!this.isAdmin) return
      try {
        this.apiKeys = await backd.admin.apiKeys.list()
      } catch (e) {
        this.error = e.message
      }
    },

    async createKey() {
      this.busy = true
      this.error = null
      this.newKey = null
      try {
        // Read-only on this database; 90 days, rotated before it expires.
        const key = await backd.admin.apiKeys.create({ name: this.newKeyName, expiresIn: '90d', scopes: ['read:main'] })
        this.newKey = key // the key shows once — copy it now
        void key
        this.newKeyName = ''
        await this.loadKeys()
        await this.loadAudit()
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    async revokeKey(name) {
      if (!confirm(`Revoke the key "${name}"? It stops working at once.`)) return
      await backd.admin.apiKeys.revoke(name).catch((e) => { this.error = e.message })
      await this.loadKeys()
      await this.loadAudit()
    },

    async loadMailbox() {
      if (!this.isAdmin) return
      try {
        const page = await outbox.list({ orderBy: '-_meta.created_at', limit: 20 })
        this.mailbox = page.items
      } catch (e) {
        this.error = e.message
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
        // publish is sync — no job to poll.
        await this.refilter()
        await this.loadMine()
        await this.loadDrafts()
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
    async loadNames() {
      try {
        const page = await members.list({ limit: 100 })
        this.names = Object.fromEntries(page.items.map((m) => [m.user_id, m.email]))
      } catch {
        // no members collection yet (before chapter 8): cards just say nothing.
        // (One page of 100 is plenty for a demo; a real app would look up the
        // ids on screen with `where: { user_id: { $in: [...] } }`.)
      }
    },

    // "published by …" for an asset whose publish function stamped
    // `published_by` (the optional schema step of chapter 7); '' otherwise.
    byline(asset) {
      const who = asset.published_by && this.names[asset.published_by]
      return who ? `published by ${who}` : ''
    },

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
        // Admin invoke: works for internal functions too (no HTTP route).
        const input = this.fnName === 'digest' ? { since_days: 7 } : {}
        const out = await backd.admin.invokeFunction(`main/${this.fnName}`, { input })
        if (out && typeof out.status === 'function') {
          // Async: a Job — poll until done.
          this.jobId = out.id
          while ((this.jobStatus = await out.status()) !== 'done') {
            await new Promise((r) => setTimeout(r, 500))
          }
          this.jobResult = out.data?.result
        } else {
          this.jobStatus = 'done'
          this.jobResult = out
        }
        await this.loadNotifs()
        await this.loadShares()
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    async requestReset() {
      this.busy = true
      this.error = null
      try {
        await backd.auth.requestPasswordReset({ email: this.email })
        this.accountMsg = 'If that address has an account, a reset email is on its way — see the mailbox.'
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    async requestEmailChange() {
      this.busy = true
      this.accountMsg = null
      try {
        await backd.auth.requestEmailChange({ newEmail: this.newEmail, password: this.pwd.current })
        this.newEmail = ''
        this.accountMsg = 'Check the new address — a confirmation email was sent (the mailbox in dev).'
        await this.loadMailbox()
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
