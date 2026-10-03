// The blog example: Alpine.js + backd-js, no build step.
// Everyone sees published posts; signed-in users also see their drafts,
// and can edit, publish, unpublish or delete their own posts. Posts show who
// wrote them and their category; clicking an author or a category lists
// only those posts (both filters combine). The rules that
// decide all this live in examples/config/blog/main/posts/rules.yaml.
import { createClient, localStorageStorage, VersionMismatchError } from 'backd-js'

const backd = createClient({
  // The page and the API share one origin (nginx in docker-compose.yml),
  // so no CORS settings are needed.
  url: window.location.origin,
  realm: 'blog',
  // Keep the session across reloads. Any script on the page can read
  // localStorage: real apps must guard against cross-site scripting.
  storage: localStorageStorage('backd-blog-example'),
})
const db = backd.db('main')
const posts = db.collection('posts')

document.addEventListener('alpine:init', () => {
  window.Alpine.data('blog', () => ({
    user: null,
    mode: 'login',
    email: '',
    password: '',
    draft: emptyDraft(),
    editing: null, // the post being edited: { id, title, body, category }
    posts: [],
    filters: { author: null, category: null }, // null: no filter
    // Counts a reader couldn't compute themselves — see main/_functions/stats
    // and the docs page "Blog" for why this needs a server-side function.
    stats: { published: 0, mine: 0 },
    error: '',
    busy: false,

    async init() {
      backd.auth.onAuthChange((event) => {
        if (event === 'SESSION_EXPIRED') {
          this.user = null
          this.error = 'Your session ended. Please log in again.'
          this.load()
        }
      })
      if (await backd.auth.token()) {
        try {
          this.user = await backd.auth.me()
        } catch {
          // The stored session expired; onAuthChange handles it.
        }
      }
      await this.load()
      await this.refreshStats()
    },

    // Best-effort: a stats hiccup shouldn't block the rest of the page.
    async refreshStats() {
      try {
        this.stats = await db.fn('stats')
      } catch {
        // Leave the previous counts showing.
      }
    },

    // Categories seen in the loaded posts, suggested when writing a post.
    get categories() {
      return [...new Set(this.posts.map((p) => p.category).filter(Boolean))].sort()
    },

    isMine(post) {
      return this.user !== null && post._meta.owner === this.user.id
    },

    // Runs an action, showing its error if it fails.
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

    load() {
      return this.run(async () => {
        this.posts = await fetchPosts(this.filters)
      })
    },

    // Sets one filter ('author' or 'category'; null removes it) and lists
    // the matching posts. The server does the filtering.
    filterBy(key, value) {
      this.filters = { ...this.filters, [key]: value }
      return this.load()
    },

    clearFilters() {
      this.filters = { author: null, category: null }
      return this.load()
    },

    submitAuth() {
      return this.run(async () => {
        const credentials = { email: this.email, password: this.password }
        const session = this.mode === 'signup' ? await backd.auth.signup(credentials) : await backd.auth.login(credentials)
        this.user = session.user
        this.password = ''
        this.posts = await fetchPosts(this.filters)
        await this.refreshStats()
      })
    },

    logout() {
      return this.run(async () => {
        await backd.auth.logout()
        this.user = null
        this.posts = await fetchPosts(this.filters)
        await this.refreshStats()
      })
    },

    create() {
      return this.run(async () => {
        const { category, ...fields } = this.draft
        const doc = { ...fields, author: { email: this.user.email } } // the rules refuse any other email
        const c = normalizeCategory(category)
        if (c) doc.category = c
        const post = await posts.create(doc)
        if (matches(post, this.filters)) this.posts.unshift(post)
        this.draft = emptyDraft()
        await this.refreshStats()
      })
    },

    startEdit(post) {
      this.editing = { id: post.id, title: post.title, body: post.body ?? '', category: post.category ?? '' }
    },

    saveEdit(post) {
      return this.run(async () => {
        const { title, body, category } = this.editing
        // A JSON merge patch: null removes the category.
        await this.update(post, { title, body, category: normalizeCategory(category) || null })
        this.editing = null
      })
    },

    togglePublished(post) {
      return this.run(async () => {
        await this.update(post, { published: !post.published })
        await this.refreshStats()
      })
    },

    // Applies a change to one of the user's posts. ifMatch makes it fail
    // instead of overwriting a change made meanwhile (in another tab, say).
    async update(post, patch) {
      try {
        const updated = await posts.patch(post.id, patch, { ifMatch: post._meta.version })
        this.posts = this.posts.flatMap((p) => (p.id !== post.id ? [p] : matches(updated, this.filters) ? [updated] : []))
      } catch (err) {
        if (!(err instanceof VersionMismatchError)) throw err
        this.editing = null
        this.posts = await fetchPosts(this.filters)
        throw new Error('This post changed meanwhile; it was reloaded. Try again.')
      }
    },

    remove(post) {
      if (!confirm(`Delete "${post.title}"?`)) return
      return this.run(async () => {
        await posts.delete(post.id)
        this.posts = this.posts.filter((p) => p.id !== post.id)
        await this.refreshStats()
      })
    },
  }))
})

// The latest posts the caller may read, narrowed by the active filters.
async function fetchPosts(filters) {
  const where = {}
  if (filters.author) where['author.email'] = filters.author
  if (filters.category) where.category = filters.category
  return (await posts.list({ where, orderBy: '-_meta.created_at', limit: 50 })).items
}

function matches(post, filters) {
  return (!filters.author || post.author?.email === filters.author) && (!filters.category || post.category === filters.category)
}

// "  News " and "news" are the same category.
function normalizeCategory(value) {
  return (value ?? '').trim().toLowerCase()
}

function emptyDraft() {
  return { title: '', body: '', category: '', published: true }
}

// A readable message for any error: backd's own message, plus the fields
// that failed validation.
function describe(err) {
  const fields = (err.details ?? []).map((d) => `${d.path} ${d.reason}`)
  return [err.message, ...fields].join(' · ')
}
