// Shelf — the tutorial app (docs: Tutorial).
//
// Chapter 5 adds the first functions: curators `publish` drafts, members
// mint `share` links, and `?s=<token>` resolves one publicly through
// `share-open` — the holes the rules chapter left, closed.
//
// Chapter 20 makes a team an organization: people create teams, invite each
// other with a link (or by email), and a team's pins are readable only by its
// members. `?join=<token>` offers an invitation to a signed-in member.
//
// Chapter 19 types it: `backd types` writes backd/types.d.ts, `main` and the
// collections below are typed from it in an editor, and on localhost the client
// sends the types' fingerprint, so a schema change the app hasn't been
// regenerated for shows up as a warning in the console.
//
// Chapter 26 lets a member download a copy of everything Shelf holds about them:
// the account page asks for it (with a fresh proof), follows it, saves the zip and
// shows the README inside it. The email that says it is ready links `/?export=<id>`.
//
// Chapter 18 reads the realm's app configuration: a maintenance banner anyone
// sees, and a switch that hides the export button (the server enforces it too).
// The page follows the configuration live, so a change shows up without a reload.
//
// Chapter 16 adds a way in without a password (a link or code by email) and
// a second factor: the step after the password, and the account page's
// security section.
//
// Chapters 13–15 add files: an asset can carry one file (`file`, through backd)
// and up to five attachments (straight to the bucket, with a progress bar), the
// worker makes a thumbnail of an image (a declared version), downloads are counted by the
// `download` function, and a shared asset comes with links to its files.
import { createClient, expand, localStorageStorage, NotFoundError, RetryableError, SecondStepRequiredError, VersionMismatchError, versionStatus } from 'backd-js'
import { readZip } from './lib/zip.js'

/** @typedef {import('./backd/types').Realm} Realm */

// The fingerprint of the types this app was written against (backd/realm.json). Only a
// development build sends it: elsewhere there is no header and no warning. No top-level
// await here: the module must finish before Alpine's deferred script runs, or it misses
// `alpine:init` below and nothing is registered. The client's `types` is read per
// request, so it can be attached when the fetch lands.
/** @type {import('backd-js').TypedClient<Realm>} */
const root = /** @type {any} */ (createClient({
  url: window.location.origin,
  realm: 'shelf',
  // Sessions survive reloads. In a real app, guard against XSS before
  // trusting localStorage (or use cookie sessions).
  storage: localStorageStorage('shelf'),
}))
// Chapter 20: a session belongs to one team, and the team's slug is the name people type to sign in
// to it. This page works in one team at a time: `backd` is that team's client (the one without a
// team only signs in, and signs in the realm's operators), and a change of team reloads the page.
const TEAM_KEY = 'shelf.team'
const params0 = new URLSearchParams(window.location.search)
/** The team this page works in: the address (?team=), else the last one used. */
const teamRef = (params0.get('team') ?? window.localStorage.getItem(TEAM_KEY) ?? '').trim().toLowerCase()
/** @type {import('backd-js').TypedClient<Realm>} */
const backd = teamRef ? root.org(teamRef) : root
const clientFor = (/** @type {string} */ ref) => (ref.trim() ? root.org(ref) : root)
/** The client a sign-in in progress started with: its second step and its session belong there. */
let signing = backd
/** Remembers the team to open and loads the page again in it. */
function openTeamPage(/** @type {string} */ ref, hash = '#mine') {
  window.localStorage.setItem(TEAM_KEY, ref.trim().toLowerCase())
  window.history.replaceState({}, '', window.location.pathname + hash)
  window.location.reload()
}
if (['localhost', '127.0.0.1'].includes(window.location.hostname)) {
  fetch('./backd/realm.json').then((r) => r.json()).then((i) => { root.types = i.fingerprint; backd.types = i.fingerprint }).catch(() => undefined)
}
const main = backd.db('main')
const assets = main.collection('assets')
const shares = main.collection('shares')
const notifications = main.collection('notifications')
const members = main.collection('members')
const pins = main.collection('pins')
// The open team's live list (chapter 24), kept out of the component so Alpine doesn't wrap it.
/** @type {import('backd-js').LiveView | null} */
let liveTeam = null
// Chapter 24: the gallery's live list, and what the stream doesn't carry. A streamed asset
// comes as stored: no file links and no expanded author, so `hydrate` reads those for many
// assets at once and keeps them by id, with the files they were read for (`sig`).
/** @type {import('backd-js').LiveView | null} */
let liveGallery = null
/** @type {any[]} */
let galleryItems = []
/** @type {Map<string, { sig: string, file?: any, attachments?: any[], _expanded?: any }>} */
const galleryExtras = new Map()
const hydrating = new Set()
/** @type {ReturnType<typeof setInterval> | null} */
let galleryTimer = null
const GALLERY_PAGE = 9
const LINK_REFRESH_MS = 4 * 60 * 1000 // file links expire after five minutes
const AUTHOR = expand(['author', ['name', 'avatar']])
const filesSig = (/** @type {any} */ d) => JSON.stringify([d.file?.id ?? null, versionStatus(d.file, 'thumb').status, (d.attachments ?? []).map((/** @type {any} */ f) => f.id)])
const profiles = main.collection('profiles')
const outbox = backd.db('mail').collection('outbox')

const VIEWS = ['gallery', 'mine', 'teams', 'review', 'notifications', 'admin', 'auth', 'account']

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
    // Chapter 20: the team people sign in to (its slug; empty for an operator), what a new
    // team is called when a person starts one, and what the sign-in page can offer for it.
    company: teamRef,
    teamName: '',
    methods: ['password', 'magic_link'],
    companyMsg: null,
    invitation: '', // signup: invite — the token the operator hands over
    // Chapter 16: the step after the password (a code of the second factor, or
    // setting one up), and the code of a magic-link email.
    step: null, // { kind: 'mfa' | 'enroll', challenge, methods, setup, recovery }
    code: '',
    magicSent: false,
    // Chapter 17: the consents the realm asks for (name, required, version and
    // text), what the sign-up form ticked, and what the account page shows.
    consentList: [],
    ticked: {}, // name → true
    consentState: null,
    // Chapter 26: my exports (null: this realm does not export), what the page says about
    // the one in progress, and the README of the one just saved.
    exports: null,
    exportMsg: null,
    exportReadme: null,
    exportFocus: null, // the id the email linked to
    // Chapter 18: the app configuration (`client.config`): the banner everyone
    // sees, and the switch that shows the export button.
    maintenance: { enabled: false, message: '' },
    exportEnabled: true,
    // The account page: my second factor, and what setting it up shows.
    mfa: null,
    mfaSetup: null,
    recoveryCodes: null,

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
    pwd: { next: '' },
    accountMsg: null,

    // Notifications: mine, newest first (functions write them; I can only
    // flip read_at — the rules say so).
    notifs: [],

    // Teams (chapter 20): the team this page works in (its id, name and my roles there), the
    // teams this browser holds a session for, its pins and members, and what an invitation
    // made (a link to copy).
    teams: [], // [{ ref, id, name }]: the sessions of this browser
    team: null, // the open team's id
    org: null, // { id, name, slug, roles, teams } of the team this page works in
    pinList: [],
    liveStatus: 'connecting', // the pins' stream: connecting, live, resyncing, closed
    dashboard: [],
    dashboardZone: 'UTC',
    teamMembers: [],
    teamSlug: '',
    pinDraft: { title: '', url: '', note: '', private_notes: '' },
    invite: { email: '', roles: 'editor', link: null, sent: false },
    joinOffer: null, // { token, org, roles }: what ?join=<token>&org=<id> offers
    joinToken: null,
    joinOrg: null,

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
    // Chapter 24: the gallery's stream (connecting, live, resyncing, closed), `static` while
    // a search is shown, `not live` when the collection can't be followed.
    galleryStatus: '',
    galleryLimit: GALLERY_PAGE,
    tag: '',
    sort: '-published_at',
    // Chapter 22: a search text; when set the gallery is a search, best match first.
    q: '',
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
      // An invitation to a team (?join=<token>&org=<id>): the person signs in to the team with it
      // (or makes an account), and joins as part of that.
      this.joinToken = params.get('join')
      this.joinOrg = params.get('org')
      if (this.joinToken && this.joinOrg) await this.previewJoin()
      else this.joinToken = null
      await this.loadConsentTexts()
      await this.loadConfig()
      // Back on the tab: the copy is fresh for its cache_ttl, then revalidated with a 304.
      window.addEventListener('focus', () => this.loadConfig())
      backd.config.on('change', () => this.readConfig())
      // And instantly: backd tells this page when what it reads of the configuration changes.
      backd.config.live(['maintenance', 'features'])
      backd.config.on('error', () => {}) // no realtime on this server: focus and cache_ttl still work
      // The email that says an export is ready links the account page with ?export=<id>.
      this.exportFocus = params.get('export')
      if (this.exportFocus) window.location.hash = '#account'
      // The recovery codes shown once, kept across the page load that opened the team.
      try {
        this.recoveryCodes = JSON.parse(window.sessionStorage.getItem('shelf.recovery') ?? 'null')
        window.sessionStorage.removeItem('shelf.recovery')
      } catch {
        this.recoveryCodes = null
      }
      // ?s=<token> is the public share view — no session needed.
      const s = params.get('s')
      if (s) await this.openShare(s)
      // A magic link ends here with a code in the address: the client redeems it
      // when it is created, and `ready` is the outcome — the session, or a step.
      try {
        const outcome = await root.auth.ready
        if (outcome && 'challenge' in outcome) {
          signing = root
          await this.startStep(outcome)
        } else if (outcome && 'org' in outcome && outcome.org) {
          // The link signed in to a team: its session is that team's client's, named by its id.
          openTeamPage(outcome.org)
          return
        }
      } catch (e) {
        this.error = e.message
      }
      try {
        this.user = await backd.auth.me()
        this.org = this.user.org ?? null
      } catch (e) {
        this.user = null
        this.open = e instanceof NotFoundError
      }
      if (!this.user) await this.loadOptions()
      await this.refilter()
      if (this.user) await Promise.all([this.loadMine(), this.loadDrafts(), this.loadInvites(), this.loadShares(), this.loadNotifs(), this.ensureMember(), this.loadAdmin(), this.loadMfa(), this.loadConsents(), this.loadExports()])
      if (this.user) await this.loadNames()
      if (this.user) await this.loadTeams()
      this.syncView()
    },

    // Chapter 18: what the user may read of the configuration. Fresh copies cost
    // no request; a stale one is revalidated, usually with a 304.
    async loadConfig() {
      try {
        await backd.config.load(['maintenance', 'features'])
      } catch {
        // Offline or refused: the banner and the button keep their last state.
      }
      this.readConfig()
    },

    readConfig() {
      this.maintenance = {
        enabled: backd.config.getSync('maintenance.enabled', false) === true,
        message: backd.config.getSync('maintenance.message', ''),
      }
      // Signed out, `features` isn't readable: the button isn't there either.
      this.exportEnabled = backd.config.getSync('features.export.enabled', false) === true
    },

    // The export function answers one JSON document; the browser saves it.
    async exportMine() {
      this.busy = true
      this.error = null
      try {
        const out = await main.fn('export')
        const blob = new Blob([JSON.stringify(out, null, 2)], { type: 'application/json' })
        const a = Object.assign(document.createElement('a'), { href: URL.createObjectURL(blob), download: 'my-assets.json' })
        a.click()
        URL.revokeObjectURL(a.href)
      } catch (e) {
        // The switch was turned off since the page last looked: the server refused.
        this.error = e.message
        await this.loadConfig()
      } finally {
        this.busy = false
      }
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
        // Chapter 17: the consents ticked, by name and the version shown.
        if (this.mode === 'signup' && this.consentList.length) {
          const missing = this.consentList.filter((c) => c.required && !this.ticked[c.name])
          if (missing.length) throw new Error('Please accept: ' + missing.map((c) => c.name).join(', '))
          credentials.consents = Object.fromEntries(this.consentList.filter((c) => this.ticked[c.name]).map((c) => [c.name, c.version]))
        }
        // Chapter 20: the team of an invitation link, else the one typed; empty is the operators' door.
        const ref = (this.joinToken ? this.joinOrg : this.company) ?? ''
        signing = clientFor(ref)
        const step = (/** @type {any} */ e) => {
          if (e instanceof SecondStepRequiredError) {
            return { challenge: e.challenge, enrollmentRequired: e.code === 'mfa_enrollment_required', consentRequired: e.code === 'consent_required', consents: e.consents, methods: e.methods }
          }
          throw e
        }
        let result
        if (this.mode === 'signup' && this.joinToken) {
          // Joining a team that invited you: an account for it, with its invitation.
          result = await signing.auth.signup({ ...credentials, invitation: this.joinToken }).catch(step)
        } else if (this.mode === 'signup') {
          // Starting a team: the operator's invitation (chapter 4) lets a person in, and the team is theirs.
          if (!this.teamName.trim() || !ref.trim()) throw new Error('Name your team and choose its short name')
          result = await signing.auth.signUpWithOrg({ ...credentials, name: this.teamName.trim(), slug: ref.trim().toLowerCase(), invitation: this.invitation || undefined }).catch(step)
        } else {
          // An invitation is used as part of the sign-in.
          result = await signing.auth.signIn(this.joinToken ? { ...credentials, invitation: this.joinToken } : credentials)
        }
        this.password = ''
        this.invitation = ''
        await this.afterFirstStep(result)
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    // A session, or the step that comes before one.
    async afterFirstStep(result) {
      if ('challenge' in result) {
        await this.startStep(result)
        return
      }
      await this.signedIn(result)
    },

    async startStep(result) {
      this.mode = 'login'
      window.location.hash = '#auth'
      if (result.consentRequired) {
        // Chapter 17: terms to accept before the session: show their texts.
        const consents = []
        for (const c of result.consents) {
          const t = await backd.consents.text(c.name, c.version)
          consents.push({ ...c, text: t.text })
        }
        this.ticked = {}
        this.step = { kind: 'consent', challenge: result.challenge, consents }
        return
      }
      if (!result.enrollmentRequired) {
        this.step = { kind: 'mfa', challenge: result.challenge, methods: result.methods, setup: null, recovery: false }
        return
      }
      // The role requires a second factor and the user has none: set one up now.
      const setup = await signing.auth.mfa.setup({ challenge: result.challenge })
      this.step = { kind: 'enroll', challenge: result.challenge, methods: [], setup, recovery: false }
    },

    // The session is kept by the client of the team signed in to (the operators' by the one
    // without a team): open the page there, so everything loads as that team's member.
    async signedIn(session, hash = '#mine') {
      this.step = null
      this.code = ''
      this.magicSent = false
      openTeamPage(signing.orgRef || session.org || '', this.joinToken ? '#teams' : hash)
    },

    // The code of the authenticator app (or a recovery code) after the password.
    async submitCode() {
      this.busy = true
      this.error = null
      try {
        const method = this.step.recovery ? 'recovery_code' : 'totp'
        await this.signedIn(await signing.auth.verifyMfa(this.step.challenge, method, this.code.trim()))
      } catch (e) {
        this.error = e.message
        this.code = ''
      } finally {
        this.busy = false
      }
    },

    // First sign-in of a role that requires a second factor: the first code
    // proves the app works, and the recovery codes are shown once.
    async confirmEnrollment() {
      this.busy = true
      this.error = null
      try {
        const done = await signing.auth.mfa.confirm({ challenge: this.step.challenge, setupId: this.step.setup.setup_id, code: this.code.trim() })
        this.recoveryCodes = done.recovery_codes
        if (done.consent) {
          // The terms come before the session (chapter 17); the codes wait on the account page.
          await this.startStep(done.consent)
          return
        }
        // The recovery codes are shown once, on the account page: keep them for the page that opens.
        window.sessionStorage.setItem('shelf.recovery', JSON.stringify(done.recovery_codes))
        await this.signedIn(done.session, '#account')
      } catch (e) {
        this.error = e.message
        this.code = ''
      } finally {
        this.busy = false
      }
    },

    // --- magic link ----------------------------------------------------------

    // Always answers the same, whoever the address is: the page doesn't tell.
    async requestMagicLink() {
      this.busy = true
      this.error = null
      try {
        signing = clientFor(this.company)
        await signing.auth.magicLink.request(this.email)
        this.magicSent = true
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    // Typed in this app, on any device that can read the mail. The link in the
    // same email works only here, in this browser (the mailbox view shows both).
    async submitMagicCode() {
      this.busy = true
      this.error = null
      try {
        await this.afterFirstStep(await signing.auth.magicLink.verifyCode(this.code.trim()))
      } catch (e) {
        this.error = e.message
        this.code = ''
      } finally {
        this.busy = false
      }
    },

    async logout() {
      this.closeGallery()
      galleryExtras.clear()
      await backd.auth.logout().catch(() => {})
      // The page opens again in the same team (its name stays in the sign-in form), signed out of it.
      openTeamPage(teamRef, '#gallery')
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
        const common = {
          limit: GALLERY_PAGE,
          // Chapter 13: every file comes with a link, for the previews shown at
          // once. They expire (five minutes), so downloads fetch a fresh one.
          fileLinks: true,
          // Chapter 21: each asset's author, in the same request. Only the name
          // and the avatar come back; visitors who can't read profiles get null.
          expand: AUTHOR,
        }
        // Chapter 22: with a search text the gallery asks the text index, best match
        // first, and pages by offset; without one it is the list of before.
        const page = this.q.trim()
          ? await assets.search(this.q.trim(), { ...common, where: this.where(), skip: this.assets.length })
          : await assets.list({ ...common, where: this.where(), orderBy: this.sort, after: this.after })
        this.assets = this.assets.concat(page.items)
        this.after = page.next_cursor
        this.hasMore = page.has_more
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    // A new filter or sort restarts the list from the first page. Without a search text the
    // gallery is live (chapter 24); a search is a plain list until the box is cleared.
    async refilter() {
      this.closeGallery()
      this.assets = []
      this.after = undefined
      this.hasMore = false
      if (!this.q.trim() && (this.user || this.open)) {
        this.galleryLimit = GALLERY_PAGE
        this.openGallery()
        return
      }
      this.galleryStatus = this.q.trim() ? 'static' : ''
      await this.load()
    },

    // Load more: a live list grows (and is followed again with the larger limit); a
    // static one reads the next page.
    async loadMore() {
      if (liveGallery) {
        this.galleryLimit += GALLERY_PAGE
        this.openGallery()
        return
      }
      await this.load()
    },

    // After the app changed an asset: a live gallery hears it from the stream; a static
    // one reads again.
    async refresh() {
      if (!liveGallery) await this.refilter()
    },

    // Chapter 24: the published assets, live. The read rule is checked on every change, so a
    // curator's publish makes the asset appear for every member, and a delete removes it.
    openGallery() {
      this.closeGallery()
      this.galleryStatus = 'connecting'
      const view = assets.subscribe({ where: this.where(), load: { sort: this.sort, limit: this.galleryLimit } })
      liveGallery = view
      view.on('change', (state) => {
        if (liveGallery !== view || state.status === 'connecting') return
        galleryItems = state.items
        this.showLive()
      })
      view.on('status', (s) => {
        if (liveGallery === view) this.galleryStatus = s
      })
      view.on('error', async (e) => {
        if (liveGallery !== view) return
        // `realtime: true` isn't on the assets yet (or the stack doesn't stream): the plain list.
        this.closeGallery()
        this.assets = []
        this.after = undefined
        this.galleryStatus = 'not live'
        await this.load()
        if (e.code !== 'realtime_not_enabled' && e.status !== 404 && !this.error) this.error = e.message
      })
      galleryTimer = setInterval(() => this.hydrate(galleryItems), LINK_REFRESH_MS)
    },

    closeGallery() {
      const view = liveGallery
      liveGallery = null
      view?.close()
      if (galleryTimer) clearInterval(galleryTimer)
      galleryTimer = null
      galleryItems = []
    },

    // The live list as cards: each asset with the links and author read for its files.
    // Assets not read yet (or whose files changed) are read in one batch.
    showLive() {
      const missing = []
      this.assets = galleryItems.map((d) => {
        const held = galleryExtras.get(d.id)
        const same = held?.sig === filesSig(d)
        if (!same) missing.push(d)
        if (!held) return d
        return { ...d, _expanded: held._expanded, ...(same ? { file: held.file, attachments: held.attachments } : {}) }
      })
      this.hasMore = galleryItems.length >= this.galleryLimit
      if (missing.length) this.hydrate(missing)
    },

    // One list read per 100 assets, with file links and the author: what a streamed asset lacks.
    async hydrate(items) {
      const want = items.filter((d) => !hydrating.has(d.id))
      if (!want.length) return
      for (const d of want) hydrating.add(d.id)
      try {
        for (let i = 0; i < want.length; i += 100) {
          const chunk = want.slice(i, i + 100)
          /** @type {Map<string, any>} */
          let found = new Map()
          try {
            const page = await assets.list({ where: { id: { $in: chunk.map((d) => d.id) } }, limit: chunk.length, fileLinks: true, expand: AUTHOR })
            found = new Map(page.items.map((d) => [d.id, d]))
          } catch {
            // The cards show without links for now; the next change or refresh asks again.
          }
          // Kept under the files of the asset that was asked for, so a read that is ahead
          // of the stream doesn't ask again until the stream's update arrives.
          for (const d of chunk) {
            const got = found.get(d.id)
            galleryExtras.set(d.id, { sig: filesSig(d), file: got?.file ?? d.file, attachments: got?.attachments ?? d.attachments, _expanded: got?._expanded })
          }
        }
      } finally {
        for (const d of want) hydrating.delete(d.id)
      }
      if (liveGallery) this.showLive()
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

    // The picture an asset's card shows: the thumbnail once a worker made it,
    // else the image itself — both links came with the list (`fileLinks`).
    previewUrl(asset) {
      return asset.file?.versions?.thumb?.url ?? (this.isImage(asset.file) ? asset.file.url : null)
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
        setTimeout(() => this.refresh(), 1500) // the count moved
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

    // Chapter 14: the thumbnail is made by a worker after the upload; the app
    // doesn't wait for it, it looks again a few times until the version is ready
    // (`versionStatus` reads the state from the document).
    async watchThumbnail(assetId, tries = 8) {
      if (tries === 0) return
      await new Promise((r) => setTimeout(r, 1500))
      try {
        const doc = await assets.get(assetId)
        if (versionStatus(doc.file, 'thumb').status !== 'pending') {
          await Promise.all([this.loadMine(), this.refresh()])
          return
        }
      } catch {
        return // a thumbnail is a nicety: the asset is saved either way
      }
      return this.watchThumbnail(assetId, tries - 1)
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
        if (this.isImage(made.file)) this.watchThumbnail(made.id)
        this.draft = { title: '', kind: 'link', url: '', body: '', tags: '', file: null, attachments: [] }
        for (const input of document.querySelectorAll('input[type=file]')) input.value = ''
        await this.refresh()
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
        if (field === 'file' && this.isImage(doc.file)) this.watchThumbnail(asset.id)
        await this.loadMine()
        await this.refresh()
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
        await this.refresh()
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
        await this.refresh()
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
        await this.refresh()
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
        await this.refresh()
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

    // --- teams (chapter 20) ------------------------------------------------

    // What the sign-in page can offer for the team typed: a password always, a link by email when
    // the realm sends them. An unknown team looks like any other: the page can't say it is wrong.
    async loadOptions() {
      const ref = (this.joinOrg ?? this.company).trim()
      try {
        this.methods = ref ? (await root.auth.options({ org: ref })).methods : ['password', 'magic_link']
      } catch {
        this.methods = ['password', 'magic_link']
      }
    },

    // What a join link offers, before anybody signs in: the invitation says which team and which roles.
    async previewJoin() {
      try {
        const offer = await root.auth.previewInvitation(this.joinToken, { org: this.joinOrg })
        this.joinOffer = { token: this.joinToken, org: offer.org, roles: offer.roles }
        this.company = this.joinOrg
        window.location.hash = '#auth'
      } catch (e) {
        this.error = e.code === 'invalid_invitation' ? 'That invitation is no longer valid.' : e.message
        this.joinToken = null
      }
    },

    // "Which teams am I in?" for someone who does not remember the short name: the answer goes to
    // the mailbox of the address, and tells this page nothing.
    async findTeams() {
      this.busy = true
      this.error = null
      try {
        await root.auth.findOrgs({ email: this.email })
        this.companyMsg = 'If that address belongs to teams, an email with the links is on its way — see the mailbox.'
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    // The team this page works in, and the teams this browser holds a session for (the switcher).
    async loadTeams() {
      if (!this.user) return
      try {
        const held = await root.orgs.signedIn()
        this.teams = await Promise.all(held.map(async (h) => {
          const org = h.ref === teamRef ? this.org : await root.org(h.ref).auth.org().catch(() => null)
          return { ref: h.ref, id: org?.id ?? h.id, name: org?.name ?? h.name ?? h.ref }
        }))
        await this.openTeam(this.org?.id ?? null)
      } catch (e) {
        if (e.code !== 'not_found') this.error = e.message
      }
    },

    // The team being open: its pins, kept live (chapter 24: a subscription filtered by the
    // team, which the read rule narrows to what this member may see) and its members.
    async openTeam(id) {
      liveTeam?.close()
      liveTeam = null
      this.team = id
      this.pinList = []
      this.teamMembers = []
      this.liveStatus = 'connecting'
      this.invite.link = null
      if (!id) return
      liveTeam = pins.subscribe({ load: { sort: '-_meta.created_at', limit: 50 } })
      liveTeam.on('change', (state) => {
        this.pinList = state.items
      })
      liveTeam.on('status', (s) => {
        this.liveStatus = s
      })
      // A reload after a reset found that this member can no longer read the team: the list is
      // empty and the team's page says so; signing in again is what they do next.
      liveTeam.on('access_lost', async () => {
        this.error = 'You are no longer a member of this team.'
      })
      liveTeam.on('error', async (e) => {
        // Chapter 24 hasn't set `realtime: true` on pins yet (or the stack
        // doesn't stream): fall back to the plain list, without live updates.
        if (e.code === 'realtime_not_enabled' || e.status === 404) {
          liveTeam?.close()
          liveTeam = null
          try {
            const page = await pins.list({ orderBy: '-_meta.created_at', limit: 50 })
            if (this.team === id) this.pinList = page.items
            this.liveStatus = 'not live'
          } catch (err) {
            this.error = /** @type {Error} */ (err).message
          }
          return
        }
        this.error = e.message
      })
      const members = await backd.orgs.members(id).list()
      this.teamMembers = members.items
      await this.loadDashboard()
    },

    // Chapter 22: pins per member per month, summed up by the database in the team's
    // time zone (a claim an operator sets; UTC when it has none). The rules still
    // decide what is counted: a member's totals cover only what they may read.
    async loadDashboard() {
      this.dashboard = []
      if (!this.team) return
      try {
        const org = await backd.orgs.get(this.team)
        const tz = typeof org.claims?.timezone === 'string' ? org.claims.timezone : 'UTC'
        this.dashboardZone = tz
        const { groups } = await pins.aggregate({
          groupBy: ['author', '_meta.created_at:month'],
          metrics: ['count'],
          tz,
          orderBy: ['-_meta.created_at:month', '-count'],
          limit: 24,
        })
        this.dashboard = groups.map((g) => ({
          month: g.key['_meta.created_at:month'],
          who: this.names[g.key.author] || g.key.author || 'unknown',
          count: g.count,
        }))
      } catch (e) {
        this.error = e.message
      }
    },

    get currentTeam() {
      return this.org
    },

    // The roles I hold in the open team decide what the page offers; the
    // server decides what is allowed.
    holds(...roles) {
      return roles.some((r) => this.org?.roles?.includes(r))
    },

    // Another team of this browser: the page opens there (its session is already here).
    switchTeam(ref) {
      openTeamPage(ref, '#teams')
    },

    // Starting a team gives a session in it at once: no password to type again.
    async createTeam() {
      this.busy = true
      this.error = null
      try {
        const slug = this.teamSlug.trim().toLowerCase() || this.teamName.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
        const org = await backd.orgs.create({ name: this.teamName.trim(), slug })
        this.teamName = ''
        this.teamSlug = ''
        openTeamPage(org.slug ?? org.id, '#teams')
      } catch (e) {
        this.error = e.code === 'slug_taken' ? 'That short name is taken: choose another.' : e.message
      } finally {
        this.busy = false
      }
    },

    // The pin function checks the plan: a free team holds five pins.
    async addPin() {
      this.busy = true
      this.error = null
      try {
        const { title, url, note, private_notes } = this.pinDraft
        await main.fn('pin', { org: this.team, title, ...(url ? { url } : {}), ...(note ? { note } : {}), ...(private_notes ? { private_notes } : {}) })
        this.pinDraft = { title: '', url: '', note: '', private_notes: '' }
        await this.loadDashboard() // the list updates itself, from the stream
      } catch (e) {
        this.error = e.code === 'plan_limit' ? `${e.message}. An operator can put the team on the pro plan.` : e.message
      } finally {
        this.busy = false
      }
    },

    async removePin(id) {
      this.error = null
      try {
        await pins.delete(id)
        await this.loadDashboard()
      } catch (e) {
        this.error = e.message
      }
    },

    // The answer to an invitation holds the link once: show it, to copy, and
    // for an address also have the server email it.
    async inviteToTeam(byEmail) {
      this.busy = true
      this.error = null
      try {
        const roles = this.invite.roles ? [this.invite.roles] : []
        const input = byEmail
          ? { binding: 'email', email: this.invite.email, roles, send: true }
          : { binding: 'none', roles, maxUses: 5 }
        const made = await backd.orgs.invitations(this.team).create(input)
        this.invite.link = made.link
        this.invite.sent = byEmail
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    async setMemberRoles(user, roles) {
      this.error = null
      try {
        await backd.orgs.members(this.team).update(user, roles)
        await this.openTeam(this.team)
      } catch (e) {
        this.error = e.message
      }
    },

    // Leaving ends access at once. The last owner can't leave: say so.
    async leaveTeam() {
      this.error = null
      try {
        await backd.orgs.leave(this.team)
        // Leaving ends the session's access at once; the page opens signed out of the team.
        await backd.auth.logout().catch(() => {})
        openTeamPage(teamRef, '#gallery')
      } catch (e) {
        this.error = e.code === 'last_org_manager' ? 'You are the last owner: make someone else an owner, or delete the team.' : e.message
      }
    },

    // Deleting a team takes its pins with it, and asks for a fresh proof.
    async deleteTeam() {
      if (!window.confirm(`Delete ${this.currentTeam?.name} and its ${this.pinList.length} pins?`)) return
      this.error = null
      try {
        await backd.auth.withProof(
          async (methods) => this.askProof(methods),
          () => backd.orgs.delete(this.team),
        )
        await backd.auth.logout().catch(() => {})
        openTeamPage(teamRef, '#gallery')
      } catch (e) {
        this.error = e.message
      }
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
        this.names = Object.fromEntries(page.items.map((m) => [m.id, m.email]))
      } catch {
        // no members collection yet (before chapter 8): cards just say nothing.
        // (One page of 100 is plenty for a demo; a real app would look up the
        // ids on screen with `where: { id: { $in: [...] } }`.)
      }
    },

    // "published by …" for an asset whose publish function stamped
    // `published_by` (the optional schema step of chapter 7); '' otherwise.
    byline(asset) {
      const parts = []
      // Chapter 21: the author comes with the list (`expand`), null when the
      // profile is missing or the reader can't see it.
      const author = asset._expanded?.author
      if (author?.name) parts.push(`${author.avatar ? author.avatar + ' ' : ''}by ${author.name}`)
      const who = asset.published_by && this.names[asset.published_by]
      if (who) parts.push(`published by ${who}`)
      return parts.join(' · ')
    },

    async ensureMember() {
      if (!this.user) return
      try {
        // The member's document has the user's own id (custom_id: user).
        const mine = await members.get(this.user.id).catch((e) => (e.status === 404 ? null : Promise.reject(e)))
        if (!mine) {
          await members.create({ id: this.user.id, email: this.user.email })
        } else if (mine.email !== this.user.email) {
          await members.patch(this.user.id, { email: this.user.email })
        }
        await this.ensureProfile()
      } catch (e) {
        this.error = e.message
      }
    },

    // Chapter 21: one profile per user, at the user's own id (`custom_id: user`): there is at
    // most one because an id is unique. It starts with the name before the @; the member can
    // change it.
    async ensureProfile() {
      try {
        await profiles.get(this.user.id)
      } catch (e) {
        if (!(e instanceof NotFoundError)) throw e
        await profiles.create({ id: this.user.id, name: this.user.email.split('@')[0] })
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
        await clientFor(this.company).auth.requestPasswordReset({ email: this.email })
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
        // A sensitive action: prove who you are again first (the client keeps the proof for 5 minutes).
        await backd.auth.withProof((methods) => this.askProof(methods), () => backd.auth.requestEmailChange({ newEmail: this.newEmail }))
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
        await backd.auth.withProof((methods) => this.askProof(methods), () => backd.auth.changePassword({ newPassword: this.pwd.next }))
        this.pwd = { next: '' }
        this.accountMsg = 'Password changed.'
      } catch (e) {
        this.accountMsg = e.message
      } finally {
        this.busy = false
      }
    },

    async deleteAccount() {
      if (!confirm('Deactivate your account? Your assets follow the collection\'s erasure policy.')) return
      this.busy = true
      try {
        await backd.auth.withProof((methods) => this.askProof(methods), () => backd.auth.deleteAccount())
        await this.logout()
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },
    // --- second factor and proofs (chapter 16) ------------------------------

    // The server says which proofs it takes (`methods`); a person with a second
    // factor gives a code, anyone else the password. null cancels the action.
    async askProof(methods) {
      const useCode = methods.includes('totp')
      const answer = prompt(useCode ? 'Code from your authenticator app:' : 'Confirm with your password:')
      if (!answer) throw new Error('cancelled')
      return useCode ? { method: 'totp', code: answer.trim() } : { method: 'password', password: answer }
    },

    // --- consents (chapter 17) --------------------------------------------------

    // What the realm asks for, with the text of each current version (public).
    async loadConsentTexts() {
      try {
        const list = await backd.consents.current()
        this.consentList = await Promise.all(list.map(async (c) => ({
          name: c.name, required: c.required, version: c.current.version,
          text: (await backd.consents.text(c.name, c.current.version)).text,
        })))
      } catch {
        this.consentList = [] // a realm without consents, or without auth
      }
    },

    // The sign-in step: accept what it names (and, if ticked, the optional ones).
    async submitConsents() {
      this.busy = true
      this.error = null
      try {
        const accepted = {}
        for (const c of this.step.consents) {
          if (!this.ticked[c.name]) throw new Error('Please accept the ' + c.name)
          accepted[c.name] = c.version
        }
        for (const c of this.consentList) if (!c.required && this.ticked[c.name]) accepted[c.name] = c.version
        const session = await signing.auth.acceptConsents(this.step.challenge, accepted)
        if (this.recoveryCodes) window.sessionStorage.setItem('shelf.recovery', JSON.stringify(this.recoveryCodes))
        await this.signedIn(session, this.recoveryCodes ? '#account' : '#mine')
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    async loadConsents() {
      try {
        this.consentState = await backd.consents.state()
      } catch {
        this.consentState = null
      }
    },

    // The account page's switch for an optional consent (the digest by email).
    async toggleConsent(c) {
      this.busy = true
      this.error = null
      try {
        this.consentState = this.consentState.consents[c.name]
          ? await backd.consents.withdraw(c.name)
          : await backd.consents.accept({ [c.name]: c.version })
      } catch (e) {
        this.error = e.message
      } finally {
        this.busy = false
      }
    },

    // --- my data (chapter 26) -----------------------------------------------------

    // My exports, newest first. A realm that does not export answers 404: the card is hidden.
    async loadExports() {
      try {
        this.exports = await backd.account.exports()
      } catch {
        this.exports = null
      }
    },

    // Asks for one (a fresh proof first: the password, or the second factor's code) and follows it
    // to the end; the email says so too, for a member who closes the page.
    async askExport() {
      this.busy = true
      this.exportMsg = null
      this.exportReadme = null
      try {
        const job = await backd.auth.withProof((methods) => this.askProof(methods), () => backd.account.requestExport())
        await this.followExport(job.id)
      } catch (e) {
        this.exportMsg =
          e instanceof RetryableError
            ? `You asked for a copy in the last 24 hours: try again in ${Math.ceil((e.retryAfter ?? 0) / 3600000)} hour(s), or use the one below.`
            : e.message
      } finally {
        this.busy = false
        await this.loadExports()
      }
    },

    async followExport(id) {
      await backd.account.waitForExport(id, {
        pollIntervalMs: 1500,
        onProgress: (e) => (this.exportMsg = e.step ? e.step.replace(/^Export: /, 'Preparing: ') : 'Waiting for the worker…'),
      })
      this.exportMsg = 'Your data is ready.'
    },

    // Saves the zip and shows the README inside it, read from the package itself.
    async saveExport(id) {
      this.busy = true
      this.exportMsg = null
      try {
        const download = await backd.account.downloadExport(id)
        const bytes = await download.bytes()
        this.exportReadme = await readZip(bytes).text('README.html')
        const a = Object.assign(document.createElement('a'), {
          href: URL.createObjectURL(new Blob([bytes], { type: 'application/zip' })),
          download: 'shelf-my-data.zip',
        })
        a.click()
        URL.revokeObjectURL(a.href)
        this.exportFocus = null
      } catch (e) {
        this.exportMsg = e.message
      } finally {
        this.busy = false
      }
    },

    async loadMfa() {
      try {
        this.mfa = await backd.auth.mfa.status()
      } catch {
        this.mfa = null
      }
    },

    async startMfa() {
      this.busy = true
      this.accountMsg = null
      try {
        this.mfaSetup = await backd.auth.withProof((methods) => this.askProof(methods), () => backd.auth.mfa.setup())
      } catch (e) {
        this.accountMsg = e.message
      } finally {
        this.busy = false
      }
    },

    async confirmMfa() {
      this.busy = true
      this.accountMsg = null
      try {
        const done = await backd.auth.mfa.confirm({ setupId: this.mfaSetup.setup_id, code: this.code.trim() })
        this.recoveryCodes = done.recovery_codes
        this.mfaSetup = null
        this.code = ''
        await this.loadMfa()
      } catch (e) {
        this.accountMsg = e.message
      } finally {
        this.busy = false
      }
    },

    async disableMfa() {
      this.busy = true
      this.accountMsg = null
      try {
        await backd.auth.withProof((methods) => this.askProof(methods), () => backd.auth.mfa.disable())
        await this.loadMfa()
      } catch (e) {
        this.accountMsg = e.message
      } finally {
        this.busy = false
      }
    },
  }))
})
