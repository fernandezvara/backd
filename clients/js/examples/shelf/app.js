// Shelf — the tutorial app (docs: Tutorial).
//
// Chapter 1 wires the public gallery and the "new asset" form to the realm;
// every later chapter replaces one more static section with live bindings.
import { createClient } from 'backd-js'

const backd = createClient({ url: window.location.origin, realm: 'shelf' })
const assets = backd.db('main').collection('assets')

document.addEventListener('alpine:init', () => {
  Alpine.data('shelf', () => ({
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
      await this.refilter()
    },

    // `{"tags": "docs"}` finds documents whose tags array contains "docs".
    where() {
      return this.tag ? { tags: this.tag } : {}
    },

    async load() {
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
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },
  }))
})
