// Walks the shelf tutorial with the same client library the app uses, without
// a browser, and checks what the chapters say should hold: the public gallery,
// drafts and the rules around published_at, an invitation-gated sign-up, the
// publish/notify call chain, share links, the async digest and the emails it
// queues, a signed import webhook, API keys and the audit trail. It is the
// tutorial's regression test.
//
//   start the tutorial stack (the compose file of the docs), then:
//   make shelf-tour           # or:
//   BACKD_URL=http://localhost:8080 node clients/js/examples/shelf/tour.js
//
// signup is invite-only, so on a fresh stack the tour bootstraps the operator
// through `docker compose exec` (it needs to reach the stack's backd service —
// set SHELF_COMPOSE to the compose file if it isn't the default project) and
// then creates its own invitations. It can run again and again: every account
// and event id is new or looked up first. Exits 0 when every check passes.
import { execFileSync } from 'node:child_process'
import { createHmac } from 'node:crypto'
import { createClient, AuthenticationError, ForbiddenError, NotFoundError } from '../../src/index.js'

const url = process.env.BACKD_URL ?? 'http://localhost:8080'
const PASSWORD = 'dev-p4ssw0rd!'
const OPERATOR = 'operator@shelf.example'
const CURATOR = 'curator@shelf.example'
const MEMBER = `ana-${Date.now().toString(36)}@shelf.example`
const WEBHOOK_SECRET = 'dev-secret'

let failed = 0
/** @param {string} what @param {boolean} ok @param {string} [detail] */
function check(what, ok, detail = '') {
  if (!ok) failed++
  console.log(`  ${ok ? '✓' : '✗'} ${what}${ok || !detail ? '' : `\n      ${detail}`}`)
}
/** Runs an action that must be refused with this error class and code. */
async function refused(/** @type {string} */ what, /** @type {() => Promise<unknown>} */ action, /** @type {Function} */ Class, /** @type {string} */ code) {
  try {
    await action()
    check(what, false, 'it succeeded')
  } catch (err) {
    const e = /** @type {any} */ (err)
    check(`${what} (${e.status} ${e.code})`, err instanceof Class && (!code || e.code === code), `${e.name}: ${e.message}`)
  }
}
const heading = (/** @type {string} */ text) => console.log(`\n${text}`)

/** Signs in; with no account yet, invites self through the operator and signs up. */
async function signedIn(/** @type {string} */ email, /** @type {any} */ operator) {
  const backd = createClient({ url, realm: 'shelf' })
  try {
    await backd.auth.login({ email, password: PASSWORD })
    return backd
  } catch (err) {
    if (/** @type {any} */ (err).status !== 401) throw err
  }
  const invite = await operator.admin.invitations.create({ email })
  await backd.auth.signup({ email, password: PASSWORD, invitation: invite.token })
  return backd
}

// The operator is a different case: signup is invite-only and only an admin
// can invite, so on a fresh stack it exists only once bootstrap creates it.
async function operatorClient() {
  const backd = createClient({ url, realm: 'shelf' })
  try {
    await backd.auth.login({ email: OPERATOR, password: PASSWORD })
    return backd
  } catch (err) {
    if (/** @type {any} */ (err).status !== 401) throw err
  }
  const compose = process.env.SHELF_COMPOSE ? ['compose', '-f', process.env.SHELF_COMPOSE] : ['compose']
  const args = [...compose, 'exec', '-T', 'backd', '/backd', 'bootstrap', '--realm', 'shelf', '--email', OPERATOR]
  try {
    execFileSync('docker', args, { input: PASSWORD + '\n', stdio: ['pipe', 'ignore', 'pipe'] })
    console.log(`  (bootstrapped ${OPERATOR} through docker compose exec)`)
  } catch (e) {
    console.log(`no operator yet and bootstrap failed (${/** @type {any} */(e).message}).`)
    console.log(`Run by hand in the stack's directory:\n  echo ${PASSWORD} | docker compose exec -T backd /backd bootstrap --realm shelf --email ${OPERATOR}`)
    process.exit(1)
  }
  await backd.auth.login({ email: OPERATOR, password: PASSWORD })
  return backd
}

const anonymous = createClient({ url, realm: 'shelf' })
const operator = await operatorClient()
const curator = await signedIn(CURATOR, operator)
const member = await signedIn(MEMBER, operator)
const main = (/** @type {any} */ c) => c.db('main')

heading('The accounts (ch2, ch4)')
check('the operator holds the admin role', (await operator.auth.me()).roles.includes('admin'))
check('the curator holds the curator role', (await curator.auth.me()).roles.includes('curator'))
check('the member has no role', (await member.auth.me()).roles.length === 0)
await refused('signup without an invitation is refused', () => createClient({ url, realm: 'shelf' }).auth.signup({ email: `no-${MEMBER}`, password: PASSWORD }), ForbiddenError, '')

heading('Drafts, the gallery and publish (ch1, ch3, ch5)')
const draft = await main(member).collection('assets').create({ title: 'The backd handbook', kind: 'link', url: 'https://example.com/handbook' })
const queue = (await main(curator).collection('assets').list({ where: { published_at: null }, orderBy: '-_meta.created_at', limit: 50 })).items
check("the curator's review queue lists a member's draft", queue.some((a) => a.id === draft.id))
await refused('a member may not stamp published_at', () => main(member).collection('assets').create({ title: 'x', kind: 'note', published_at: new Date().toISOString() }), ForbiddenError, '')
await refused('the gallery needs a sign-in (rules: read needs a user)', () => main(anonymous).collection('assets').list(), AuthenticationError, 'unauthenticated')
await refused('a member may not publish (curators only)', () => main(member).fn('publish', { asset_id: draft.id }), ForbiddenError, '')
const pub = /** @type {any} */ (await main(curator).fn('publish', { asset_id: draft.id }, { idempotencyKey: `publish-${draft.id}` }))
check('the curator published it', typeof pub.published_at === 'string')
check('it is in the gallery for every signed-in member now', (await main(member).collection('assets').get(draft.id)).published_at === pub.published_at)

heading('Notifications and the call chain (ch7)')
await refused('notify has no HTTP route — it is internal', () => main(member).fn('notify', { to_user: 'x', kind: 'x', text: 'x' }), NotFoundError, 'not_found')
const notifs = await main(member).collection('notifications').list({ where: { to_user: (await member.auth.me()).id } })
const published = notifs.items.find((n) => n.kind === 'asset.published' && n.asset_id === draft.id)
check('publish notified the owner', published !== undefined && !published.read_at, JSON.stringify(notifs.items))

heading('Share links (ch5)')
const share = /** @type {any} */ (await main(member).fn('share', { asset_id: draft.id, expires_in: 'week' }, { idempotencyKey: `share-${draft.id}-${Date.now()}` }))
check('the member minted a link', typeof share.token === 'string' && share.token.length >= 16, JSON.stringify(share))
const opened = /** @type {any} */ (await main(anonymous).fn('share-open', { token: share.token }))
check('a stranger resolves it publicly', opened.title === draft.title && opened.kind === draft.kind && !('_meta' in opened), JSON.stringify(opened))
const mine = (await main(member).collection('shares').list()).items.find((s) => s.token === share.token)
check('the member lists only their own link', mine !== undefined, '')
await main(member).collection('shares').delete(mine.id)
await refused('a revoked link is gone', () => main(anonymous).fn('share-open', { token: share.token }), NotFoundError, '')

heading('The digest as a job, with email (ch8, ch9, ch10)')
// What the app does on every sign-in: the members directory a function reads.
const me = await member.auth.me()
const myDir = await main(member).collection('members').list({ where: { user_id: me.id }, limit: 1 })
if (myDir.items.length === 0) await main(member).collection('members').create({ user_id: me.id, email: MEMBER })
const job = /** @type {any} */ (await operator.admin.invokeFunction('main/digest', { input: { since_days: 7 } }))
const result = /** @type {any} */ (await job.wait({ pollIntervalMs: 300, timeoutMs: 90000 }))
check('the digest job ran and notified', result.notified >= 1, JSON.stringify(result))
const digestNotifs = (await main(member).collection('notifications').list({ where: { to_user: me.id, kind: 'digest' } })).items
check('the member got the in-app digest', digestNotifs.length >= 1, JSON.stringify(digestNotifs))
// email-capture is the delivery function in the dev stack: the digest mail
// lands in mail/outbox, which admins may read.
const mails = (await operator.db('mail').collection('outbox').list({ where: { kind: 'shelf-digest', to: MEMBER }, limit: 5 })).items
check('the digest mail is in the dev mailbox', mails.some((m) => m.to.includes(MEMBER)), JSON.stringify(mails))

heading('The import webhook (ch11)')
const known = (await operator.admin.secrets.list()).some((s) => s.name === 'IMPORT_WEBHOOK_SECRET' && s.database === 'main')
if (!known) await operator.admin.secrets.set('IMPORT_WEBHOOK_SECRET', WEBHOOK_SECRET, { database: 'main' })
async function webhook(/** @type {string} */ body, /** @type {string} */ secret = WEBHOOK_SECRET) {
  const signature = `sha256=${createHmac('sha256', secret).update(body).digest('hex')}`
  const res = await fetch(`${url}/v1/shelf/main/_func/import`, { method: 'POST', headers: { 'content-type': 'application/json', 'x-signature': signature }, body })
  return { status: res.status, text: (await res.text()).trim() }
}
const eventId = `feed-tour-${Date.now().toString(36)}`
const body = JSON.stringify({ event_id: eventId, title: `Pushed by the tour ${eventId}`, url: 'https://example.com/pushed' })
// The function reads its secret within about a minute of it being set.
let first = await webhook(body)
for (let i = 0; i < 70 && first.status === 500; i++) {
  await new Promise((r) => setTimeout(r, 1000))
  first = await webhook(body)
}
check('a signed push lands a draft (200 ok)', first.status === 200 && first.text === 'ok', JSON.stringify(first))
// The draft belongs to nobody (a webhook has no user): the curator's read
// rule (chapter 5) is what lets the Review view list it.
const reviewQueue = (await main(curator).collection('assets').list({ where: { published_at: null }, orderBy: '-_meta.created_at', limit: 50 })).items
const imported = reviewQueue.find((a) => a.title === `Pushed by the tour ${eventId}`)
check('a plain member does not see an imported draft', (await main(member).collection('assets').list({ where: { title: `Pushed by the tour ${eventId}` } })).items.length === 0)
check('the curator sees it in the review queue, still a draft', imported !== undefined && !imported.published_at, JSON.stringify(imported))
const again = await webhook(body)
const copies = (await main(curator).collection('assets').list({ where: { title: `Pushed by the tour ${eventId}` } })).items.length
check('the same event again is ignored (200 already processed), and makes no second asset', again.status === 200 && again.text === 'already processed' && copies === 1, JSON.stringify({ again, copies }))
const wrong = await webhook(JSON.stringify({ event_id: `${eventId}-x`, title: 'forged' }), 'not-the-secret')
check('a wrong signature is refused (400 invalid signature)', wrong.status === 400 && wrong.text === 'invalid signature', JSON.stringify(wrong))

heading('API keys and the audit trail (ch12)')
const keyName = `tour-${Date.now().toString(36)}`
const key = /** @type {any} */ (await operator.admin.apiKeys.create({ name: keyName, expiresIn: '90d', scopes: ['read:main'] }))
check('the key is shown once, prefixed bdk_', key.key.startsWith('bdk_'))
const keyed = createClient({ url, realm: 'shelf', apiKey: key.key })
check('the key reads the collection it is scoped to', (await keyed.db('main').collection('assets').list({ limit: 1 })).items.length >= 0)
await refused('a read-scoped key may not write', () => keyed.db('main').collection('assets').create({ title: 'x', kind: 'note' }), ForbiddenError, '')
await operator.admin.apiKeys.revoke(keyName)
await refused('a revoked key is dead at once', () => keyed.db('main').collection('assets').list(), AuthenticationError, '')
const audit = await operator.admin.audit.list({ limit: 30 })
check('the trail records the key', audit.items.some((e) => e.action === 'apikey.create' && e.target === `key:${keyName}`), JSON.stringify(audit.items[0]))
check('the trail records the hand-run digest', audit.items.some((e) => e.action.startsWith('function.invoke') && e.target === 'main/digest'), '')

heading('cleanup by hand (ch9)')
const sweep = /** @type {any} */ (await operator.admin.invokeFunction('main/cleanup'))
const swept = /** @type {any} */ (await sweep.wait({ pollIntervalMs: 300, timeoutMs: 90000 }))
check('cleanup runs and reports', typeof swept.shares === 'number' && typeof swept.notifications === 'number', JSON.stringify(swept))

console.log(`\n${failed === 0 ? 'All checks passed.' : `${failed} check(s) failed.`}`)
process.exit(failed === 0 ? 0 : 1)
