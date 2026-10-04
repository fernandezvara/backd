// Shelf — the tutorial app (docs: Tutorial).
//
// Chapter 3 adds rules and editing: my-assets entries can be edited and
// deleted, and every write carries If-Match — a 412 means somebody else
// wrote first.
import { createClient, localStorageStorage, VersionMismatchError } from 'backd-js'

const backd = createClient({
  url: window.location.origin,
  realm: 'shelf',
  // Sessions survive reloads. In a real app, guard against XSS before
  // trusting localStorage (or use cookie sessions).
  storage: localStorageStorage('shelf'),
})
const assets = backd.db('main').collection('assets')

document.addEventListener('alpine:init', () => {
  Alpine.data('shelf', () => ({
    // Session: null while signed out.
    user: null,
    mode: 'login', // login | signup
    email: '',
    password: '',

    // My assets and the account page.
    mine: [],
    editing: null, // { id, version, title, url, body, tags } — the card being edited
    pwd: { current: '', next: '' },
    accountMsg: null,

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
      try {
        this.user = await backd.auth.me()
      } catch {
        this.user = null
      }
      await this.refilter()
      if (this.user) await this.loadMine()
    },

    // --- session -----------------------------------------------------------

    async submitAuth() {
      this.busy = true
      this.error = null
      try {
        const credentials = { email: this.email, password: this.password }
        const session = this.mode === 'signup'
          ? await backd.auth.signup(credentials)
          : await backd.auth.login(credentials)
        this.user = session.user
        this.password = ''
        await this.refilter()
        await this.loadMine()
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
        const doc = {
          title: this.draft.title,
          kind: this.draft.kind,
          published_at: new Date().toISOString(),
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
