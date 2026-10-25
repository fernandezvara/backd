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
// A session belongs to one team (chapter 20): every account the tour makes starts a team of its own with
// the operator's invitation, and signs in to it by its short name. Only the operator, who runs the realm
// and belongs to no team, signs in without one. Signup is invite-only, so on a fresh stack the tour bootstraps the operator
// through `docker compose exec` (it needs to reach the stack's backd service —
// set SHELF_COMPOSE to the compose file if it isn't the default project) and
// then creates its own invitations. It can run again and again: every account
// and event id is new or looked up first. Exits 0 when every check passes.
import { execFileSync } from 'node:child_process'
import { createHmac } from 'node:crypto'
import { mkdtempSync, readFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { deflateSync } from 'node:zlib'
import { createClient, expand, fillBuckets, AuthenticationError, ConflictError, ConsentRequiredError, ForbiddenError, IdTakenError, InvalidIdError, NotFoundError, RetryableError, SecondStepRequiredError, ValidationError, VersionMismatchError, versionStatus } from '../../src/index.js'
import { readZip } from './lib/zip.js'

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

// The consents of chapters 17 and 26: the terms and the privacy notice are required (the version in
// force), and the members of the tour also opt in to the digest by email.
const consentVersions = await (async () => {
  const list = await createClient({ url, realm: 'shelf' }).consents.current()
  return Object.fromEntries(list.map((c) => [c.name, c.current.version]))
})()
const ALL_CONSENTS = { terms: consentVersions.terms, privacy: consentVersions.privacy, marketing: consentVersions.marketing }

/** The short name of the team a person starts at sign-up: the part of their address before the @. */
const teamOf = (/** @type {string} */ email) => email.split('@')[0].toLowerCase()

/**
 * Signs in to the person's own team; with no account yet, the operator invites them and they start the
 * team (the account, the team and the session in one call). The client is bound to that team.
 */
async function signedIn(/** @type {string} */ email, /** @type {any} */ operator) {
  const backd = /** @type {any} */ (createClient({ url, realm: 'shelf', org: teamOf(email) }))
  backd.tourEmail = email
  try {
    await backd.auth.login({ email, password: PASSWORD })
    return backd
  } catch (err) {
    const e = /** @type {any} */ (err)
    if (e instanceof SecondStepRequiredError && e.code === 'consent_required') {
      // An account from before the terms existed: accept them to get in.
      await backd.auth.acceptConsents(e.challenge, ALL_CONSENTS)
      return backd
    }
    if (e.status !== 401) throw err
  }
  const invite = await operator.admin.invitations.create({ email })
  await backd.auth.signUpWithOrg({ email, password: PASSWORD, name: `${teamOf(email)}'s team`, slug: teamOf(email), invitation: invite.token, consents: ALL_CONSENTS })
  return backd
}

/** A person's client in another team: signs in to it with the invitation that brings them in. */
async function joinedTeam(/** @type {any} */ person, /** @type {string} */ slug, /** @type {string} */ invitation) {
  const team = person.org(slug)
  await team.auth.login({ email: person.tourEmail, password: PASSWORD, invitation })
  return team
}

// The admin role requires a second factor (chapter 16): the operator's first
// sign-in sets up an authenticator app, which the tour plays with a TOTP
// computed from the secret (RFC 6238), and keeps the recovery codes to answer
// the proof that sensitive actions ask for.
const B32 = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ234567'
/** The 6-digit code of a base32 secret at a time. */
function totp(/** @type {string} */ secret, at = Date.now()) {
  let bits = ''
  for (const ch of secret.replace(/=+$/, '').toUpperCase()) bits += B32.indexOf(ch).toString(2).padStart(5, '0')
  const key = Buffer.from(bits.match(/.{8}/g).map((b) => parseInt(b, 2)))
  const counter = Buffer.alloc(8)
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 30000)))
  const mac = createHmac('sha1', key).update(counter).digest()
  const o = mac[19] & 15
  return String((mac.readUInt32BE(o) & 0x7fffffff) % 1000000).padStart(6, '0')
}

/** Finishes a sign-in whose first step was a password: set up the factor when asked for one. */
async function enrol(/** @type {any} */ backd, /** @type {any} */ result) {
  if (!result.enrollmentRequired) throw new Error(`expected the enrolment step, got ${JSON.stringify(result)}`)
  const setup = await backd.auth.mfa.setup({ challenge: result.challenge })
  const done = await backd.auth.mfa.confirm({ challenge: result.challenge, setupId: setup.setup_id, code: totp(setup.secret) })
  // The operator has not accepted the terms yet (chapter 17): the session waits for them.
  if (done.consent) await backd.auth.acceptConsents(done.consent.challenge, ALL_CONSENTS)
  return { secret: setup.secret, codes: /** @type {string[]} */ (done.recovery_codes) }
}

const composeArgs = process.env.SHELF_COMPOSE ? ['compose', '-f', process.env.SHELF_COMPOSE] : ['compose']
function inStack(/** @type {string[]} */ args, /** @type {string} */ input = '') {
  execFileSync('docker', [...composeArgs, 'exec', '-T', 'backd', '/backd', ...args], { input, stdio: ['pipe', 'ignore', 'pipe'] })
}

// The operator is a different case: signup is invite-only and only an admin
// can invite, so on a fresh stack it exists only once bootstrap creates it.
async function operatorClient() {
  const backd = createClient({ url, realm: 'shelf' })
  /** @type {any} */
  let result
  try {
    result = await backd.auth.signIn({ email: OPERATOR, password: PASSWORD })
  } catch (err) {
    if (/** @type {any} */ (err).status !== 401) throw err
    try {
      inStack(['bootstrap', '--realm', 'shelf', '--email', OPERATOR], PASSWORD + '\n')
      console.log(`  (bootstrapped ${OPERATOR} through docker compose exec)`)
    } catch (e) {
      console.log(`no operator yet and bootstrap failed (${/** @type {any} */(e).message}).`)
      console.log(`Run by hand in the stack's directory:\n  echo ${PASSWORD} | docker compose exec -T backd /backd bootstrap --realm shelf --email ${OPERATOR}`)
      process.exit(1)
    }
    result = await backd.auth.signIn({ email: OPERATOR, password: PASSWORD })
  }
  if (result.mfaRequired) {
    // A run on a stack the tour used before: its secret is gone. The break-glass
    // reset (on the instance, never over HTTP) lets the operator enrol again.
    inStack(['user', 'mfa-reset', '--local', '--realm', 'shelf', '--email', OPERATOR, '--yes'])
    console.log(`  (reset the second factor of ${OPERATOR} on the instance)`)
    result = await backd.auth.signIn({ email: OPERATOR, password: PASSWORD })
  }
  return { backd, ...(await enrol(backd, result)) }
}

const anonymous = createClient({ url, realm: 'shelf' })
const operatorLogin = await operatorClient()
const operator = operatorLogin.backd
/** A proof for a sensitive action: one of the operator's recovery codes, each good once. */
const operatorProof = () => operator.auth.reauth({ method: 'recovery_code', code: /** @type {string} */ (operatorLogin.codes.shift()) })
const curator = await signedIn(CURATOR, operator)
const member = await signedIn(MEMBER, operator)
const main = (/** @type {any} */ c) => c.db('main')

heading('The accounts (ch2, ch4)')
check('the operator holds the admin role', (await operator.auth.me()).roles.includes('admin'))
check('the curator holds the curator role', (await curator.auth.me()).roles.includes('curator'))
check('the member has no role', (await member.auth.me()).roles.length === 0)
await refused('starting a team without the operator\'s invitation is refused', () => createClient({ url, realm: 'shelf' }).auth.signUpWithOrg({ email: `no-${MEMBER}`, password: PASSWORD, name: 'No', slug: `no-${teamOf(MEMBER)}`, consents: ALL_CONSENTS }), ForbiddenError, '')
check('the member signed in to a team of their own, named by their address', (await member.auth.org())?.slug === teamOf(MEMBER), JSON.stringify(await member.auth.org()))

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
const myDir = await main(member).collection('members').get(me.id).catch((e) => (e.status === 404 ? null : Promise.reject(e)))
if (!myDir) await main(member).collection('members').create({ id: me.id, email: MEMBER })
// Custom ids (chapter 8): the document is the user's own, once, and nobody else's.
check('the member document has the user id', myDir ? myDir.id === me.id : true, '')
await refused('a member cannot create the document of another user', () => main(member).collection('members').create({ id: 'someone-else', email: MEMBER }), InvalidIdError, 'invalid_id')
await refused('a second document for the same user is refused', () => main(member).collection('members').create({ id: me.id, email: MEMBER }), IdTakenError, 'id_taken')
const job = /** @type {any} */ (await operator.admin.invokeFunction('main/digest', { input: { since_days: 7 } }))
const result = /** @type {any} */ (await job.wait({ pollIntervalMs: 300, timeoutMs: 90000 }))
check('the digest job ran and notified', result.notified >= 1, JSON.stringify(result))
const digestNotifs = (await main(member).collection('notifications').list({ where: { to_user: me.id, kind: 'digest' } })).items
check('the member got the in-app digest', digestNotifs.length >= 1, JSON.stringify(digestNotifs))
// email-capture is the delivery function in the dev stack: the digest mail
// lands in mail/outbox, which admins may read.
// The digest job queues each mail and the worker delivers it afterwards, so
// the mail can arrive a moment after the job is done: wait for it.
let mails = []
for (let i = 0; i < 100; i++) {
  mails = (await operator.db('mail').collection('outbox').list({ where: { kind: 'shelf-digest', to: MEMBER }, limit: 5 })).items
  if (mails.some((m) => m.to.includes(MEMBER))) break
  await new Promise((r) => setTimeout(r, 100))
}
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
await operatorProof()
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

/** A small valid PNG: a w×h gradient, so the thumbnail worker has something to decode. */
function png(/** @type {number} */ w, /** @type {number} */ h) {
  const crcTable = Array.from({ length: 256 }, (_, n) => { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; return c >>> 0 })
  const crc = (/** @type {Buffer} */ b) => { let c = 0xffffffff; for (const x of b) c = crcTable[(c ^ x) & 255] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0 }
  const chunk = (/** @type {string} */ type, /** @type {Buffer} */ data) => {
    const body = Buffer.concat([Buffer.from(type), data])
    const out = Buffer.alloc(8 + data.length + 4)
    out.writeUInt32BE(data.length, 0)
    body.copy(out, 4)
    out.writeUInt32BE(crc(body), 8 + data.length)
    return out
  }
  const row = Buffer.alloc(1 + w * 3)
  const raw = Buffer.alloc((1 + w * 3) * h)
  for (let y = 0; y < h; y++) {
    for (let x = 0; x < w; x++) { row[1 + x * 3] = (x * 255) / w; row[2 + x * 3] = (y * 255) / h; row[3 + x * 3] = 128 }
    row.copy(raw, y * row.length)
  }
  const head = Buffer.alloc(13)
  head.writeUInt32BE(w, 0); head.writeUInt32BE(h, 4); head[8] = 8; head[9] = 2
  return new Blob([Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', head), chunk('IDAT', deflateSync(raw)), chunk('IEND', Buffer.alloc(0))])], { type: 'image/png' })
}
const bytesOf = async (/** @type {Blob} */ b) => Buffer.from(await b.arrayBuffer())
const fetched = async (/** @type {string} */ u) => Buffer.from(await (await fetch(u)).arrayBuffer())

heading('Files on assets (ch13)')
// The storage keys are realm secrets; the tutorial sets them with `backd secret set`.
const haveKeys = (await operator.admin.secrets.list()).filter((x) => !x.database && x.name.startsWith('STORAGE_')).length === 2
if (!haveKeys) {
  await operator.admin.secrets.set('STORAGE_ACCESS_KEY', 'backd-dev')
  await operator.admin.secrets.set('STORAGE_SECRET_KEY', PASSWORD)
}
let storageCheck = await operator.admin.storage.check().catch((e) => e)
for (let i = 0; i < 70 && storageCheck instanceof Error; i++) { // the bucket is made by minio-setup, and a new secret may take a moment
  await new Promise((r) => setTimeout(r, 1000))
  storageCheck = await operator.admin.storage.check().catch((e) => e)
}
check('backd storage check passes against MinIO', !(storageCheck instanceof Error) && storageCheck.ok, JSON.stringify(storageCheck))

const assets = main(member).collection('assets')
const photo = png(640, 480)
const withFile = await assets.prepareUpload('file', photo).then((up) => assets.create({ title: 'Team photo', kind: 'file', file: up.ref }))
check('a file asset is created with a pending upload', withFile.kind === 'file' && withFile.file.type === 'image/png' && withFile.file.size === photo.size, JSON.stringify(withFile.file))
const linked = await assets.get(withFile.id, { fileLinks: true })
check('fileLinks gives a link that works without credentials', linked.file.url && (await fetched(linked.file.url)).equals(await bytesOf(photo)))
check('a plain read has no link', (await assets.get(withFile.id)).file.url === undefined)
const listed = (await assets.list({ where: { '_meta.owner': (await member.auth.me()).id }, fileLinks: true })).items.find((a) => a.id === withFile.id)
check('a list with fileLinks carries them too', Boolean(listed?.file.url))
const dl = await assets.fileUrl(withFile.id, 'file', withFile.file.id)
check('fileUrl makes a fresh link', (await fetched(dl.url)).equals(await bytesOf(photo)))
await refused('downloadFile is for browsers', () => assets.downloadFile(withFile.id, 'file', withFile.file.id), Error, '')

const replaced = await assets.uploadFile(withFile.id, 'file', new Blob(['%PDF-1.4 the handbook'], { type: 'application/pdf' }), { name: 'handbook.pdf', ifMatch: withFile._meta.version })
check('replacing the file keeps one file, with its new type', replaced.file.type === 'application/pdf' && replaced.file.name === 'handbook.pdf' && replaced._meta.version === withFile._meta.version + 1, JSON.stringify(replaced.file))
await refused('a replace against a stale version loses', () => assets.uploadFile(withFile.id, 'file', photo, { ifMatch: withFile._meta.version }), VersionMismatchError, '')
await refused('a file over 25 MiB is refused (413) before it is stored', () => assets.uploadFile(withFile.id, 'file', new Blob([new Uint8Array(25 * 1024 * 1024 + 512 * 1024)], { type: 'application/pdf' })), Error, 'payload_too_large')
await refused('a disguised executable is refused by what its bytes say (415)', () => assets.uploadFile(withFile.id, 'file', new Blob([Buffer.from('MZ\x90\x00\x03\x00\x00\x00\x04\x00\x00\x00\xff\xff')], { type: 'image/png' }), { name: 'photo.png' }), Error, 'unsupported_file_type')
const published13 = /** @type {any} */ (await main(curator).fn('publish', { asset_id: withFile.id }, { idempotencyKey: `publish-${withFile.id}` }))
await refused("someone else's upload to a published asset is refused by the update rule (403)", () => main(curator).collection('assets').uploadFile(withFile.id, 'file', photo), ForbiddenError, '')
check('the asset is published and readable by the curator, file and all', typeof published13.published_at === 'string')
const removed = await assets.deleteFile(withFile.id, 'file', replaced.file.id)
check('removing the file leaves the asset', removed.file === undefined && removed.title === 'Team photo')

heading('Big files and thumbnails (ch14)')
const big = await assets.create({ title: 'Conference recordings', kind: 'file' })
const progress = []
let withAttachments = big
for (let i = 0; i < 5; i++) {
  withAttachments = await assets.uploadFile(big.id, 'attachments', new Blob([`recording ${i}`.repeat(1000)], { type: 'text/plain' }), { name: `talk-${i}.txt`, onProgress: (p) => progress.push(p) })
}
check('five attachments went straight to the bucket', withAttachments.attachments.length === 5 && progress.length >= 5, JSON.stringify(withAttachments.attachments?.length))
await refused('a sixth is refused (409 too_many_files)', () => assets.uploadFile(big.id, 'attachments', new Blob(['x'], { type: 'text/plain' })), Error, 'too_many_files')
const one = await assets.fileUrl(big.id, 'attachments', withAttachments.attachments[2].id)
check('an attachment downloads from the storage', (await fetched(one.url)).toString().startsWith('recording 2'))

const pictureBlob = png(800, 600)
const picture = await assets.create({ title: 'Office', kind: 'file', file: (await assets.prepareUpload('file', pictureBlob)).ref })
// The thumbnail is a declared version: a worker makes it after the upload.
const ready = async (/** @type {string} */ id) => {
  for (let i = 0; i < 120; i++) {
    const doc = await assets.get(id, { fileLinks: true })
    if (versionStatus(doc.file, 'thumb').status !== 'pending') return doc
    await new Promise((r) => setTimeout(r, 1000))
  }
  throw new Error('the thumbnail was never made')
}
const withThumb = await ready(picture.id)
const thumbState = versionStatus(withThumb.file, 'thumb')
check('a worker made the thumbnail: a small PNG of the picture', thumbState.ready && withThumb.file.versions.thumb.type === 'image/png' && withThumb.file.versions.thumb.size < withThumb.file.size, JSON.stringify(withThumb.file.versions))
check('the picture records its size, and the thumbnail its own', withThumb.file.width === 800 && withThumb.file.height === 600 && thumbState.width === 256 && thumbState.height === 256, JSON.stringify({ w: withThumb.file.width, h: withThumb.file.height, t: thumbState }))
check('the thumbnail has a link that works without credentials', Boolean(withThumb.file.versions.thumb.url) && (await fetched(withThumb.file.versions.thumb.url)).subarray(1, 4).toString() === 'PNG')
const thumbLink = await assets.fileUrl(picture.id, 'file', withThumb.file.id, { version: 'thumb' })
check('fileUrl with a version links it', Boolean(thumbLink) && (await fetched(thumbLink.url)).subarray(1, 4).toString() === 'PNG')
check('the document kept its version while the worker wrote the thumbnail', withThumb._meta.version === picture._meta.version, JSON.stringify([withThumb._meta.version, picture._meta.version]))
const notAnImage = await assets.create({ title: 'Notes', kind: 'file', file: (await assets.prepareUpload('file', new Blob(['plain notes'], { type: 'text/plain' }))).ref })
const notes = await assets.get(notAnImage.id)
check('a text file has no thumbnail: skipped, not an error', versionStatus(notes.file, 'thumb').status === 'skipped' && versionStatus(notes.file, 'thumb').reason === 'not_an_image', JSON.stringify(notes.file.versions))
check('fileUrl for a version that is not there is null', (await assets.fileUrl(notAnImage.id, 'file', notes.file.id, { version: 'thumb' })) === null)

heading('Sharing and counting downloads (ch15)')
const pub15 = await main(curator).fn('publish', { asset_id: picture.id }, { idempotencyKey: `publish-${picture.id}` })
void pub15
const fresh = await assets.get(picture.id)
const counted = /** @type {any} */ (await main(curator).fn('download', { document_id: picture.id, field: 'file', file_id: fresh.file.id }))
check('the download function returns a link to the file', (await fetched(counted.url)).equals(await bytesOf(pictureBlob)), '')
await main(curator).fn('download', { document_id: picture.id, field: 'file', file_id: fresh.file.id })
check('the downloads are counted (2)', (await assets.get(picture.id)).downloads === 2, JSON.stringify((await assets.get(picture.id)).downloads))
await refused('a signed-out caller gets no link (401)', () => main(anonymous).fn('download', { document_id: picture.id, field: 'file', file_id: fresh.file.id }), AuthenticationError, '')
await refused('an owner may not set their own count (403)', () => assets.patch(picture.id, { downloads: 1000 }), ForbiddenError, '')
await refused('nor start an asset with one (403)', () => assets.create({ title: 'Cheat', kind: 'note', downloads: 99 }), ForbiddenError, '')
const shareOfFile = /** @type {any} */ (await main(member).fn('share', { asset_id: picture.id, expires_in: 'week' }, { idempotencyKey: `share-${picture.id}-${Date.now()}` }))
const sharedFiles = /** @type {any} */ (await main(anonymous).fn('share-open', { token: shareOfFile.token }))
check('a share link opens with links to the files', sharedFiles.files.length === 1 && sharedFiles.files[0].name && !('id' in sharedFiles.files[0]), JSON.stringify(sharedFiles.files))
check('the shared file downloads without an account', (await fetched(sharedFiles.files[0].url)).subarray(1, 4).toString() === 'PNG')
const usage = await operator.admin.storage.status()
check('the operator sees the storage in use', usage.configured && usage.keys?.ok && usage.reachable?.ok && (usage.usage?.files ?? 0) >= 5 && (usage.usage?.bytes ?? 0) > 0, JSON.stringify(usage.usage))
const dry = await operator.admin.storage.reconcile()
check('reconcile reports without deleting', dry.delete === false && dry.deleted === 0, JSON.stringify(dry))

heading('Magic links and a second factor (ch16)')
const mailboxCode = async (/** @type {string} */ kind, /** @type {string} */ to) => {
  for (let i = 0; i < 100; i++) {
    const page = await operator.db('mail').collection('outbox').list({ where: { kind, to }, orderBy: '-_meta.created_at', limit: 1 })
    const found = page.items[0]?.text.match(/\b(\d{6})\b/)
    if (found) return found[1]
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`no ${kind} email with a code for ${to} in the mailbox`)
}
const linkClient = createClient({ url, realm: 'shelf' })
await linkClient.auth.magicLink.request(MEMBER)
const viaLink = /** @type {any} */ (await linkClient.auth.magicLink.verifyCode(await mailboxCode('magic-link', MEMBER)))
check('the code of the magic-link email signs a member in, no password', viaLink.user?.email === MEMBER, JSON.stringify(viaLink))
await refused('a stranger asking for a link is told nothing (and nothing is created)', async () => {
  const c = createClient({ url, realm: 'shelf' })
  await c.auth.magicLink.request(`stranger-${MEMBER}`)
  await c.auth.magicLink.verifyCode('000000')
}, Error, '')
const opStatus = await operator.auth.mfa.status()
check('the operator has a second factor, and the role requires it', opStatus.enabled && opStatus.required, JSON.stringify(opStatus))
check('the import log opens for a session that used it (user.mfa)', Array.isArray((await main(operator).collection('imports').list()).items))
await refused('a curator may not read the import log (not an admin)', () => main(curator).collection('imports').list(), ForbiddenError, '')

// A member turns the second factor on themselves; a proof (the password) is asked first.
await member.auth.reauth({ method: 'password', password: PASSWORD })
const memberSetup = await member.auth.mfa.setup()
const memberOn = await member.auth.mfa.confirm({ setupId: memberSetup.setup_id, code: totp(memberSetup.secret) })
check('a member sets up an authenticator app and gets recovery codes', memberOn.recovery_codes.length >= 8, JSON.stringify(memberOn))
const second = createClient({ url, realm: 'shelf', org: teamOf(MEMBER) })
const step = /** @type {any} */ (await second.auth.signIn({ email: MEMBER, password: PASSWORD }))
check('the password alone no longer opens a session', step.mfaRequired === true && step.methods.includes('totp'), JSON.stringify(step))
await refused('a wrong code is refused (invalid_code)', () => second.auth.verifyMfa(step.challenge, 'totp', '000000'), ValidationError, 'invalid_code')
const memberSession = await second.auth.verifyMfa(step.challenge, 'recovery_code', memberOn.recovery_codes[0])
check('a recovery code finishes the sign-in', memberSession.user.email === MEMBER, JSON.stringify(memberSession))
await refused('deleting the account asks for a fresh proof (401 reauth_required)', () => second.auth.deleteAccount(), AuthenticationError, 'reauth_required')
// With a second factor the proof is a code too: a recovery code, each good once.
await member.auth.reauth({ method: 'recovery_code', code: memberOn.recovery_codes[1] })
await member.auth.mfa.disable()
check('the member turned it off again', !(await member.auth.mfa.status()).enabled)

heading('Consents (ch17)')
const listed17 = await anonymous.consents.current()
const terms17 = listed17.find((c) => c.name === 'terms')
check('the realm asks for the terms (required) and the digest (optional)', terms17?.required === true && listed17.find((c) => c.name === 'marketing')?.required === false, JSON.stringify(listed17))
const text17 = await anonymous.consents.text('terms', consentVersions.terms)
check('the exact text has a SHA-256, public', text17.hash.length === 64 && text17.text.includes('terms of use'), JSON.stringify(text17).slice(0, 200))
const refusedInvite = await operator.admin.invitations.create({ email: `no-terms-${MEMBER}` })
await refused('a sign-up without the terms is refused (400 consent_required)', () => createClient({ url, realm: 'shelf' }).auth.signUpWithOrg({ email: `no-terms-${MEMBER}`, password: PASSWORD, name: 'No terms', slug: `no-terms-${teamOf(MEMBER)}`, invitation: refusedInvite.token }), ConsentRequiredError, 'consent_required')
const held = await member.consents.state()
check('the member holds both, and has nothing left to accept', held.consents.terms === consentVersions.terms && held.consents.marketing === consentVersions.marketing && held.to_accept.length === 0, JSON.stringify(held))
check('the history says how: at sign-up, or at the prompt', held.history.every((h) => ['signup', 'prompt'].includes(h.method)), JSON.stringify(held.history))
await refused('the terms are required for the account: they cannot be withdrawn (409)', () => member.consents.withdraw('terms'), ConflictError, 'consent_required_for_account')

// Withdrawing the digest stops the mail, though the digest function still runs for everyone.
const countDigestMails = async () => (await operator.db('mail').collection('outbox').list({ where: { kind: 'shelf-digest', to: MEMBER }, limit: 50 })).items.length
const mailsBefore = await countDigestMails()
await member.consents.withdraw('marketing')
check('the member withdrew the digest', (await member.consents.state()).consents.marketing === undefined)
const job17 = /** @type {any} */ (await operator.admin.invokeFunction('main/digest', { input: { since_days: 7 } }))
await job17.wait({ pollIntervalMs: 300, timeoutMs: 90000 })
await new Promise((r) => setTimeout(r, 2500)) // the worker delivers the queued mails a moment later
check('no digest mail followed the withdrawal', (await countDigestMails()) === mailsBefore, `${mailsBefore} → ${await countDigestMails()}`)
const regiven = await member.consents.accept({ marketing: consentVersions.marketing })
check('accepting it again is recorded', regiven.consents.marketing === consentVersions.marketing && (await member.consents.state()).history.length >= 4, JSON.stringify(regiven))

heading('App configuration (ch18)')
// The admin API for configuration: the operator's session token, as the admin UI would use it.
const operatorToken = /** @type {string} */ (await operator.storage.get())
const adminConfig = async (/** @type {string} */ method, /** @type {string} */ path, /** @type {any} */ body, /** @type {string} */ base) => {
  const res = await fetch(`${url}/v1/shelf/_admin/app-config${path}`, {
    method,
    headers: { Authorization: `Bearer ${operatorToken}`, 'Content-Type': 'application/json', ...(base ? { 'If-Match': `"${base}"` } : {}) },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  return { status: res.status, body: /** @type {any} */ (await res.json().catch(() => ({}))) }
}
const currentVersion = async () => (await adminConfig('GET', '', undefined, '')).body.version
// Start from the defaults, whatever an earlier run left.
await adminConfig('PATCH', '', { reset: ['maintenance', 'features'] }, await currentVersion())

const visitor = createClient({ url, realm: 'shelf' })
const seen = await visitor.config.load()
check('a visitor reads the public banner, nothing else', seen.maintenance?.enabled === false && seen.features === undefined, JSON.stringify(seen))
const memberSeen = await member.config.load(['maintenance', 'features'])
check('a member also reads the features', memberSeen.features?.export?.enabled === true, JSON.stringify(memberSeen))
const exported = /** @type {any} */ (await main(member).fn('export'))
check('a member exports their assets while the switch is on', typeof exported.count === 'number' && Array.isArray(exported.assets), JSON.stringify(exported).slice(0, 160))

// The window, opened and closed by the cron functions (run by hand here).
const changed17 = /** @type {string[][]} */ ([])
visitor.config.on('change', (paths) => changed17.push(paths))
const opened17 = /** @type {any} */ (await operator.admin.invokeFunction('main/maintenance-open', { input: { message: 'Back at 04:00' } }))
await opened17.wait({ pollIntervalMs: 300, timeoutMs: 90000 })
check('the window opens', (await visitor.config.get('maintenance.enabled', { refresh: true })) === true && visitor.config.getSync('maintenance.message') === 'Back at 04:00')
check('the client announced which paths changed', changed17.length === 1 && changed17[0].includes('maintenance.enabled'), JSON.stringify(changed17))
const closed17 = /** @type {any} */ (await operator.admin.invokeFunction('main/maintenance-close'))
await closed17.wait({ pollIntervalMs: 300, timeoutMs: 90000 })
check('the window closes: the group is back at its defaults', (await visitor.config.get('maintenance.enabled', { refresh: true })) === false && visitor.config.getSync('maintenance.message') === '')

// The history names the function; revalidation costs a 304.
const hist17 = (await adminConfig('GET', '/history?limit=5')).body.history
check('the history names the function that opened the window', hist17.some((h) => h.actor === 'function:main/maintenance-open'), JSON.stringify(hist17.map((h) => h.actor)))
const first17 = await fetch(`${url}/v1/shelf/_config?paths=maintenance`)
const tag17 = first17.headers.get('ETag') ?? ''
const again17 = await fetch(`${url}/v1/shelf/_config?paths=maintenance`, { headers: { 'If-None-Match': tag17 } })
check('an unchanged banner revalidates with a 304, and is public for a CDN', again17.status === 304 && /public, max-age=\d+/.test(first17.headers.get('Cache-Control') ?? ''), `${again17.status} ${first17.headers.get('Cache-Control')}`)

// Instantly: clients that follow the configuration hear of a change without asking.
{
  const pollUntil = async (/** @type {() => boolean} */ cond, /** @type {number} */ ms = 15000) => {
    for (const end = Date.now() + ms; !cond() && Date.now() < end; ) await new Promise((r) => setTimeout(r, 50))
    return cond()
  }
  const watchers = [createClient({ url, realm: 'shelf' }), createClient({ url, realm: 'shelf', storage: member.storage })]
  const [anon17, signed17] = watchers
  const heard = watchers.map(() => /** @type {string[]} */ ([]))
  for (const [i, w] of watchers.entries()) {
    await w.config.load(['maintenance', 'features'])
    w.config.on('change', (paths) => heard[i].push(...paths))
    w.config.live(['maintenance', 'features'])
  }
  const opened = /** @type {any} */ (await operator.admin.invokeFunction('main/maintenance-open', { input: { message: 'Back at 05:00' } }))
  await opened.wait({ pollIntervalMs: 300, timeoutMs: 90000 })
  check('the window opens on a page that never asked again', await pollUntil(() => anon17.config.getSync('maintenance.message') === 'Back at 05:00' && signed17.config.getSync('maintenance.message') === 'Back at 05:00'), JSON.stringify(heard))
  const closed = /** @type {any} */ (await operator.admin.invokeFunction('main/maintenance-close'))
  await closed.wait({ pollIntervalMs: 300, timeoutMs: 90000 })
  check('and closes the same way', await pollUntil(() => anon17.config.getSync('maintenance.enabled') === false && signed17.config.getSync('maintenance.message') === ''), JSON.stringify(heard))

  const before = await currentVersion()
  await adminConfig('PATCH', '', { set: { 'features.export.enabled': false } }, before)
  check('a signed-in member hears the switch flip', await pollUntil(() => signed17.config.getSync('features.export.enabled') === false), JSON.stringify(heard[1]))
  check('the visitor was never told about it', !heard[0].some((p) => p.startsWith('features')), JSON.stringify(heard[0]))
  await adminConfig('PATCH', '', { reset: ['features'] }, await currentVersion())
  check('and hears it come back', await pollUntil(() => signed17.config.getSync('features.export.enabled') === true), JSON.stringify(heard[1]))
}

// The switch the server enforces.
let version17 = await currentVersion()
const off17 = await adminConfig('PATCH', '', { set: { 'features.export.enabled': false } }, version17)
check('an administrator turns the export off', off17.status === 200 && off17.body.changed === true, JSON.stringify(off17))
await refused('the export function refuses, whatever the app shows (403)', () => main(member).fn('export'), ForbiddenError, '')
check('the member reads the switch as off', (await member.config.get('features.export.enabled', { refresh: true })) === false)
const stale17 = await adminConfig('PATCH', '', { set: { 'maintenance.enabled': true } }, version17)
check('a change based on an old version is refused (412)', stale17.status === 412, JSON.stringify(stale17))
const invalid17 = await adminConfig('PATCH', '', { set: { 'maintenance.message': 'x'.repeat(500) } }, await currentVersion())
check('a value that breaks the schema is refused, with its path (400)', invalid17.status === 400 && invalid17.body.error.details[0].path === 'maintenance.message', JSON.stringify(invalid17))
const back17 = await adminConfig('POST', '/rollback', { version: version17 }, await currentVersion())
check('a rollback restores the earlier state as a new version', back17.status === 200 && (await main(member).fn('export')).count >= 0, JSON.stringify(back17))

heading('Types from the config (ch19)')
// The generated types are the ones the running config produces: what the app was written against.
const committed19 = JSON.parse(readFileSync(new URL('./backd/realm.json', import.meta.url), 'utf8'))
const types19 = await (await fetch(`${url}/v1/shelf/_admin/types`, { headers: { Authorization: `Bearer ${operatorToken}` } })).json()
check('the running config generates the types the app was written against',
  types19.info?.fingerprint === committed19.fingerprint && String(types19.types).includes('export interface Realm'),
  `${types19.info?.fingerprint} vs ${committed19.fingerprint}`)
const withTypes = (/** @type {string} */ fp) => fetch(`${url}/v1/shelf/_config?paths=maintenance`, { headers: { 'X-Backd-Types': fp } })
check('current types are not flagged', (await withTypes(committed19.fingerprint)).headers.get('X-Backd-Types-Stale') === null)
check('out-of-date types are flagged, and the request still works', await withTypes('0000').then((r) => r.headers.get('X-Backd-Types-Stale') === 'true' && r.ok))
let warned19 = 0
const devClient = createClient({ url, realm: 'shelf', types: '0000', onStaleTypes: () => warned19++ })
await devClient.config.load(['maintenance'], { refresh: true })
await devClient.config.load(['maintenance'], { refresh: true })
check('the client warns once', warned19 === 1, String(warned19))
const spec19 = await (await fetch(`${url}/v1/shelf/_admin/openapi.json`, { headers: { Authorization: `Bearer ${operatorToken}` } })).json()
check('the admin OpenAPI document lists the internal functions', Object.keys(spec19.paths ?? {}).includes('/v1/shelf/_admin/functions/main/maintenance-open/invoke'))
const publicSpec19 = await fetch(`${url}/v1/shelf/_openapi.json`)
check('the client document is not public by default (401)', publicSpec19.status === 401, String(publicSpec19.status))

heading('Teams as organizations (ch20)')
{
  const STAMP = Date.now().toString(36)
  const ben = await signedIn(`ben-${STAMP}@shelf.example`, operator)
  const cy = await signedIn(`cy-${STAMP}@shelf.example`, operator)
  const dee = await signedIn(`dee-${STAMP}@shelf.example`, operator)
  const benId = (await ben.auth.me()).id
  const cyId = (await cy.auth.me()).id
  const pinsOf = (/** @type {any} */ c) => c.db('main').collection('pins')

  // A session belongs to one team: signing up started a team of their own, and the session is its.
  const home = await ben.auth.org()
  check('signing up started a team, and the session belongs to it', home?.slug === `ben-${STAMP}` && home.roles.includes('owner'), JSON.stringify(home))

  // A team is an organization: its creator owns it, and holds `member` without being given it. Creating
  // one opens a session in it, beside the one in the team they came from.
  const team = await ben.orgs.create({ name: `Docs ${STAMP}`, slug: `docs-${STAMP}` })
  check('creating a team makes its creator the owner', team.roles.includes('owner') && team.roles.includes('member') && team.status === 'active', JSON.stringify(team.roles))
  const benT = ben.org(team.slug)
  check('and opens a session in it', (await benT.auth.org())?.id === team.id)
  const first = /** @type {any} */ (await main(benT).fn('pin', { org: team.id, title: 'Handbook', url: 'https://example.com/handbook' }))
  check('an owner pins, and the pin says how many fit (the free plan)', first.pins === 1 && first.limit === 5, JSON.stringify(first))

  // Joining: the invitation answers its link once; the person signs in to the team with it.
  const invite = await benT.orgs.invitations(team.id).create({ binding: 'none', roles: ['editor'], maxUses: 2 })
  check('an invitation answers a link and a token, once', invite.token.startsWith('bdo_') && String(invite.link).includes(invite.token) && String(invite.link).includes(`org=${team.id}`), String(invite.link))
  const offer = await cy.auth.previewInvitation(invite.token, { org: team.slug })
  check('the person sees what the link offers before signing in', offer.org.id === team.id && offer.roles.includes('editor'), JSON.stringify(offer))
  await refused('a stranger can\'t sign in to the team (401 invalid_credentials): nothing says it exists', () => dee.org(team.slug).auth.login({ email: dee.tourEmail, password: PASSWORD }), AuthenticationError, 'invalid_credentials')
  const cyT = await joinedTeam(cy, team.slug, invite.token)
  check('signing in with it makes them a member with the invited role', (await cyT.orgs.get(team.id)).roles.includes('editor'))
  await refused('an open link can never give a role that manages the team (400)', () => benT.orgs.invitations(team.id).create({ binding: 'none', roles: ['owner'] }), ValidationError, 'validation_error')

  // Reading: the read rule is a database filter on the caller's teams.
  check('a member reads the team\'s pins', (await pinsOf(cyT).list()).items.length === 1)
  const strangerTeam = await dee.orgs.create({ name: `Elsewhere ${STAMP}`, slug: `elsewhere-${STAMP}` })
  const deeT = dee.org(strangerTeam.slug)
  await main(deeT).fn('pin', { org: strangerTeam.id, title: 'Not yours' })
  const deeSees = (await pinsOf(deeT).list()).items
  check('another team\'s members see only their own pins', deeSees.length === 1 && deeSees[0]._meta.org === strangerTeam.id, JSON.stringify(deeSees.map((p) => p._meta.org)))
  await refused('a stranger can\'t pin into the team (403 not_org_member)', () => main(deeT).fn('pin', { org: team.id, title: 'x' }), ForbiddenError, 'not_org_member')
  await refused('nor read its organization (403 not_org_member)', () => deeT.orgs.get(team.id), ForbiddenError, 'not_org_member')

  // Roles: an editor pins and removes their own, only an admin removes other people's.
  const cyPin = /** @type {any} */ (await main(cyT).fn('pin', { org: team.id, title: 'Style guide' }))
  const benPin = (await pinsOf(benT).list()).items.find((p) => p.title === 'Handbook')
  await refused('an editor can\'t remove somebody else\'s pin (403)', () => pinsOf(cyT).delete(benPin.id), ForbiddenError, 'forbidden')
  await pinsOf(cyT).delete(cyPin.id)
  check('an editor removes their own pin', (await pinsOf(benT).list()).items.length === 1)
  await refused('a plain member can\'t pin (403)', async () => {
    const eve = await signedIn(`eve-${STAMP}@shelf.example`, operator)
    const link = await benT.orgs.invitations(team.id).create({ binding: 'none', roles: [] })
    const eveT = await joinedTeam(eve, team.slug, link.token)
    return main(eveT).fn('pin', { org: team.id, title: 'x' })
  }, ForbiddenError, 'forbidden')
  await benT.orgs.members(team.id).update(cyId, ['admin'])
  await pinsOf(cyT).delete(benPin.id)
  check('an admin removes any pin', (await pinsOf(benT).list()).items.length === 0)

  // Teams of access: an "Editors" team carries `editor`; management roles stay direct.
  const editors = await benT.orgs.teams(team.id).create({ name: 'Editors', roles: ['editor'] })
  const members = (await benT.orgs.members(team.id).list()).items
  const eveId = members.find((m) => m.user !== benId && m.user !== cyId)?.user
  const made = await benT.orgs.teams(team.id).addMember(editors.id, eveId)
  check('a team carries roles to its members, and the list says where each came from', made.effective_roles.some((r) => r.role === 'editor' && r.origin.includes('Editors')), JSON.stringify(made.effective_roles))
  await benT.orgs.teams(team.id).removeMember(editors.id, eveId)

  // The plan: a claim only operators write, read by the function that counts.
  for (let i = 0; i < 4; i++) await main(benT).fn('pin', { org: team.id, title: `Pin ${i}` })
  await main(benT).fn('pin', { org: team.id, title: 'The fifth' })
  await refused('the free plan holds five pins (409 plan_limit)', () => main(benT).fn('pin', { org: team.id, title: 'The sixth' }), ConflictError, 'plan_limit')
  await refused('a member can\'t put their own team on a better plan (403)', () => main(benT).fn('team-plan', { org: team.id, plan: 'pro' }), ForbiddenError, '')
  await main(operator).fn('team-plan', { org: team.id, plan: 'pro' })
  check('the claim is on the organization, read-only for its members', (await benT.orgs.get(team.id)).claims.plan === 'pro')
  const sixth = /** @type {any} */ (await main(benT).fn('pin', { org: team.id, title: 'The sixth' }))
  check('on the pro plan the sixth fits', sixth.pins === 6 && sixth.limit === 100, JSON.stringify(sixth))

  // The last manager stays.
  await refused('the only owner can\'t leave (409 last_org_manager)', () => benT.orgs.leave(team.id), ConflictError, 'last_org_manager')
  await benT.orgs.members(team.id).update(cyId, ['owner'])
  await benT.orgs.leave(team.id)
  check('with another owner, leaving works and ends access at once', await cyT.orgs.get(team.id).then(() => true), '')
  await refused('the one who left is no longer a member (403)', () => benT.orgs.get(team.id), ForbiddenError, 'not_org_member')

  // A second factor for the team: asked with a fresh proof, and a session without one is refused.
  await cyT.auth.reauth({ method: 'password', password: PASSWORD })
  await refused('a team can\'t require a second factor of someone who has none (403 org_requires_mfa)', () => cyT.orgs.update(team.id, { require_mfa: true }), ForbiddenError, 'org_requires_mfa')

  // Deleting: the report first, then access ends at once and the pins go.
  const report = await (await fetch(`${url}/v1/shelf/_admin/orgs/${team.id}/owned`, { headers: { Authorization: `Bearer ${operatorToken}` } })).json()
  check('the report counts what deleting would remove', report.to_delete === 6 && report.collections.some((c) => c.collection === 'pins' && c.documents === 6), JSON.stringify(report))
  await cyT.auth.reauth({ method: 'password', password: PASSWORD })
  await cyT.orgs.delete(team.id)
  await refused('a deleted team has no members left (403)', () => cyT.orgs.get(team.id), ForbiddenError, 'not_org_member')
  let pinsLeft = -1
  for (let i = 0; i < 100 && pinsLeft !== 0; i++) {
    pinsLeft = (await (await fetch(`${url}/v1/shelf/_admin/orgs/${team.id}/owned`, { headers: { Authorization: `Bearer ${operatorToken}` } })).json()).to_delete
    if (pinsLeft !== 0) await new Promise((r) => setTimeout(r, 300))
  }
  check('the cleanup removed the team\'s pins', pinsLeft === 0, String(pinsLeft))
}

heading('Authors through expansion (ch21)')
{
  const STAMP = Date.now().toString(36)
  // One profile per member, at the member's own id. The member's session token is read from the
  // client's storage to see the cost headers.
  const profiles = main(member).collection('profiles')
  const memberId = (await member.auth.me()).id
  const mine = await profiles.get(memberId).catch((e) => (e instanceof NotFoundError ? undefined : Promise.reject(e)))
  const profile = mine ?? (await profiles.create({ id: memberId, name: 'Ana', avatar: '🦊' }))
  if (mine) await profiles.patch(profile.id, { name: 'Ana', avatar: '🦊' })
  check('the profile lives at the person\'s own id', profile.id === memberId && profile._meta.owner === memberId, JSON.stringify(profile._meta))
  await refused('a second profile of the same person is refused: the id is taken (409)', () => profiles.create({ id: memberId, name: 'Ana again' }), IdTakenError, 'id_taken')
  await refused('a profile at anyone else\'s id is refused (400)', () => profiles.create({ id: 'someone-else', name: 'Not me' }), InvalidIdError, 'invalid_id')

  const asset = await main(member).collection('assets').create({ title: `Authored ${STAMP}`, kind: 'note', body: 'Written by Ana' })
  const asMember = { Authorization: `Bearer ${await member.storage.get()}` }
  const read = async (/** @type {string} */ query) => {
    const res = await fetch(`${url}/v1/shelf/main/assets/${asset.id}${query}`, { headers: asMember })
    return { res, body: /** @type {any} */ (await res.json()) }
  }

  const plain = await read('')
  check('without expand nothing is looked up', plain.body._expanded === undefined && plain.res.headers.get('X-Backd-Expand-Queries') === null)
  const all = await read('?expand=author')
  check('expand returns the author next to the asset', all.body._expanded.author.name === 'Ana' && all.body._expanded.author.avatar === '🦊' && all.body._expanded.author.id === profile.id, JSON.stringify(all.body._expanded))
  check('and the asset itself is unchanged, with its own ETag', all.body.title === asset.title && all.res.headers.get('ETag') === plain.res.headers.get('ETag') && all.res.headers.get('Cache-Control') === 'private, no-cache')
  const cost = await read('?expand=' + expand(['author', ['name']]))
  check('field selection returns the id and the fields asked for', JSON.stringify(Object.keys(cost.body._expanded.author).sort()) === '["id","name"]', JSON.stringify(cost.body._expanded))
  check('the cost is in the headers: one query, one document', cost.res.headers.get('X-Backd-Expand-Queries') === '1' && cost.res.headers.get('X-Backd-Expand-Docs') === '1', `${cost.res.headers.get('X-Backd-Expand-Queries')} ${cost.res.headers.get('X-Backd-Expand-Docs')}`)

  // The list asks for it once for the whole page.
  const page = await main(member).collection('assets').list({ where: { '_meta.owner': profile._meta.owner }, expand: 'author(name)', limit: 20 })
  check('a list expands every asset, with one query', page.items.length > 0 && page.items.every((a) => a._expanded?.author?.name === 'Ana'), JSON.stringify(page.items.map((a) => a._expanded)))

  // A reference the reader can't read is null, and so is one that doesn't exist.
  const stranger = await main(anonymous).collection('assets').get(asset.id).catch((e) => e)
  check('a visitor needs a sign-in for the asset itself (401)', stranger instanceof AuthenticationError)
  const wrong = await read('?expand=nope')
  check('an unknown reference is a 400 invalid_expand that names it', wrong.res.status === 400 && wrong.body.error.code === 'invalid_expand' && String(wrong.body.error.message).includes('nope'), JSON.stringify(wrong.body))
  await refused('where can\'t follow a reference (400 invalid_query)', () => main(member).collection('assets').list({ where: { 'author.name': 'Ana' } }), ValidationError, 'invalid_query')

  // Writes keep the asset's own ETag and ignore what was expanded.
  const patched = await main(member).collection('assets').patch(asset.id, { body: 'Edited' }, { ifMatch: asset._meta.version })
  check('a write uses the asset\'s own version and is never expanded', patched._meta.version === asset._meta.version + 1 && patched._expanded === undefined)
}

heading('Search and a dashboard (ch22)')
{
  const STAMP = Date.now().toString(36)
  const word = `quokka${STAMP}`
  const mine = main(member).collection('assets')
  await mine.create({ title: `${word} handbook`, kind: 'note', body: 'about a small marsupial', tags: ['docs'] })
  await mine.create({ title: 'Field notes', kind: 'note', body: `where the ${word} lives, with a handbook` })
  await mine.create({ title: 'Unrelated', kind: 'note', body: 'nothing to see' })

  // The text index: words in any order, the weighted title first, the score in _meta.score.
  const found = await mine.search(word, { limit: 10 })
  check('a search finds the documents with the word, best match first', found.items.length === 2 && found.items[0].title.startsWith(word), JSON.stringify(found.items.map((a) => a.title)))
  check('the score rides in _meta.score and ranks the title above the body', found.items[0]._meta.score > found.items[1]._meta.score, JSON.stringify(found.items.map((a) => a._meta.score)))
  const without = await mine.search(`${word} -field`)
  check('an excluded word leaves out the documents that have it', without.items.length === 1 && without.items[0].title.startsWith(word))
  const phrase = await mine.search(`"${word} handbook"`)
  check('a quoted phrase must appear as written', phrase.items.length === 1)
  await refused('a search can\'t be inside $or (400 invalid_query)', () => mine.list({ where: { $or: [{ $search: word }, { kind: 'link' }] } }), ValidationError, 'invalid_query')
  await refused('relevance needs a search (400 invalid_query)', () => mine.list({ orderBy: '_relevance' }), ValidationError, 'invalid_query')
  const counted = await mine.search(word, { count: true, limit: 1 })
  check('count=true counts every match, not the page', counted.total === 2 && counted.items.length === 1, JSON.stringify(counted.total))

  // The dashboard: a team's pins per member per month, in the team's time zone.
  const ben = await signedIn(`ben22-${STAMP}@shelf.example`, operator)
  const dee = await signedIn(`dee22-${STAMP}@shelf.example`, operator)
  const benId = (await ben.auth.me()).id
  const pinsOf = (/** @type {any} */ c) => c.db('main').collection('pins')
  const team = await ben.orgs.create({ name: `Dash ${STAMP}`, slug: `dash-${STAMP}` })
  const benT = ben.org(team.slug)
  await main(operator).fn('team-plan', { org: team.id, timezone: 'Pacific/Auckland' })
  check('an operator gives the team a time zone, a claim next to its plan', (await benT.orgs.get(team.id)).claims.timezone === 'Pacific/Auckland')
  await refused('a time zone has to be one (400 invalid_timezone)', () => main(operator).fn('team-plan', { org: team.id, timezone: 'Mars/Base' }), ValidationError, 'invalid_timezone')
  for (let i = 0; i < 3; i++) await main(benT).fn('pin', { org: team.id, title: `Dash pin ${i}` })
  const { groups, total_groups } = await pinsOf(benT).aggregate({
    groupBy: ['author', '_meta.created_at:month'], metrics: ['count'], tz: 'Pacific/Auckland', orderBy: '-count',
  })
  check('pins per member per month: the author, the month text and the count', total_groups === 1 && groups[0].key.author === benId && /^\d{4}-\d{2}$/.test(String(groups[0].key['_meta.created_at:month'])) && groups[0].count === 3, JSON.stringify(groups))
  // A chart wants a point for every month: group by the month alone, then fill the empty ones.
  const monthly = await pinsOf(benT).aggregate({ groupBy: '_meta.created_at:month', tz: 'Pacific/Auckland' })
  const thisMonth = String(monthly.groups[0].key['_meta.created_at:month'])
  const filled = fillBuckets(monthly.groups, { field: '_meta.created_at', unit: 'month', from: thisMonth, to: thisMonth })
  check('a chart gets its buckets filled', filled.length === 1 && filled[0].count === 3, JSON.stringify(filled))
  await refused('fillBuckets needs groups of one bucket alone', async () => fillBuckets(groups, { field: '_meta.created_at', unit: 'month', from: thisMonth, to: thisMonth }), TypeError, '')
  await refused('only declared fields can be grouped by (400 invalid_aggregate)', () => pinsOf(benT).aggregate({ groupBy: 'title' }), ValidationError, 'invalid_aggregate')
  // A member of another team counts nothing of this one.
  const other = await dee.orgs.create({ name: `Other ${STAMP}`, slug: `other-${STAMP}` })
  const deeT = dee.org(other.slug)
  const none = await pinsOf(deeT).aggregate({ groupBy: 'author' })
  check('a member of another team counts nothing of this one', none.groups.length === 0 && none.total_groups === 0, JSON.stringify(none))
  await deeT.auth.reauth({ method: 'password', password: PASSWORD })
  await deeT.orgs.delete(other.id)
  await refused('a collection that declares no aggregate: refuses (400 aggregate_not_enabled)', () => main(ben).collection('shares').aggregate(), ValidationError, 'aggregate_not_enabled')
  await benT.auth.reauth({ method: 'password', password: PASSWORD })
  await benT.orgs.delete(team.id)
}

heading('A pipeline on a status (ch23)')
{
  const STAMP = Date.now().toString(36)
  const sleep = (/** @type {number} */ ms) => new Promise((r) => setTimeout(r, ms))
  const until = async (/** @type {() => Promise<any>} */ probe) => {
    for (let i = 0; i < 150; i++) {
      const v = await probe()
      if (v) return v
      await sleep(300)
    }
    return null
  }
  const curatorId = (await curator.auth.me()).id
  const draftAsset = await main(member).collection('assets').create({ title: `Pipeline ${STAMP}`, kind: 'note', body: 'worth the gallery' })
  const mine = main(member).collection('submissions')

  // The rules decide who may start and who may move it.
  await refused('a proposal can only start as submitted (403)', () => mine.create({ asset_id: draftAsset.id, status: 'approved' }), ForbiddenError, '')
  const proposal = await mine.create({ asset_id: draftAsset.id, status: 'submitted', note: 'for the gallery' })
  await refused('the author can\'t approve their own proposal (403)', () => mine.patch(proposal.id, { status: 'approved' }), ForbiddenError, '')

  // Step 1: a trigger on created.
  const noticeOf = async (/** @type {string} */ kind) =>
    (await main(member).collection('notifications').list({ where: { kind, asset_id: draftAsset.id }, limit: 5 })).items[0]
  const received = await until(() => noticeOf('submission.received'))
  check('step 1: the member is told the proposal waits', received?.to_user === (await member.auth.me()).id && /waiting/.test(received?.text ?? ''), JSON.stringify(received))
  check('a trigger run\'s write carries its place in the chain (depth 0)', received?._meta?.chain?.depth === 0 && typeof received._meta.chain.root === 'string', JSON.stringify(received?._meta))

  // Step 2: the curator's approval is the only thing a client does.
  await main(curator).collection('submissions').patch(proposal.id, { status: 'approved' })
  const done = await until(async () => {
    const s = await main(curator).collection('submissions').get(proposal.id)
    return s.status === 'published' ? s : null
  })
  check('step 2: the function marked it published, naming the curator that approved (by)', done?.decided_by === curatorId, JSON.stringify(done))
  check('the clients\' rules never allowed that status or that field: only the pipeline wrote them', done?._meta?.updated_by?.startsWith('func:') === true, JSON.stringify(done?._meta))
  const published = await main(curator).collection('assets').get(draftAsset.id)
  check('the asset is published', typeof published.published_at === 'string')

  // Step 3 runs because step 2 wrote the status: one level deeper in the same chain.
  const thanks = await until(() => noticeOf('submission.published'))
  check('step 3: the author is thanked', /gallery/.test(thanks?.text ?? ''), JSON.stringify(thanks))
  check('and that run is one level deeper in the chain step 2 started', thanks?._meta?.chain?.depth === 1 && thanks._meta.chain.root === done._meta.chain.root, JSON.stringify({ thanks: thanks?._meta?.chain, done: done?._meta?.chain }))
  await sleep(1500)
  const again = (await main(member).collection('notifications').list({ where: { kind: 'submission.published', asset_id: draftAsset.id }, limit: 5 })).items
  check('each step ran once: one thank-you, however the changes were delivered', again.length === 1, String(again.length))
}

heading('Live pins (ch24)')
{
  const STAMP = Date.now().toString(36)
  const gus = await signedIn(`gus-${STAMP}@shelf.example`, operator)
  const hal = await signedIn(`hal-${STAMP}@shelf.example`, operator)
  const halId = (await hal.auth.me()).id
  const team = await gus.orgs.create({ name: `Live ${STAMP}`, slug: `live-${STAMP}` })
  const gusT = gus.org(team.slug)
  const invite = await gusT.orgs.invitations(team.id).create({ binding: 'none', roles: ['editor'], maxUses: 1 })
  const halT = await joinedTeam(hal, team.slug, invite.token)
  const until = async (/** @type {() => boolean} */ cond, /** @type {number} */ ms = 15000) => {
    for (const end = Date.now() + ms; Date.now() < end; ) {
      if (cond()) return true
      await new Promise((r) => setTimeout(r, 50))
    }
    return false
  }

  // Gus follows the team's pins; Hal pins and Gus sees it without asking.
  const live = gusT.db('main').collection('pins').subscribe({ load: { sort: '-_meta.created_at', limit: 50 } })
  const state = await live.ready
  check('a live list loads first, then follows (status live)', state.status === 'live' && state.items.length === 0, state.status)
  await main(halT).fn('pin', { org: team.id, title: 'Appears at once' })
  check('a member\'s pin shows up for the others without a reload', await until(() => live.items.some((p) => p.title === 'Appears at once')))
  const pinId = live.items[0].id
  await halT.db('main').collection('pins').delete(pinId)
  check('a removal reaches the list too', await until(() => live.items.length === 0))

  // A stranger can't subscribe to the team.
  const ivy = await signedIn(`ivy-${STAMP}@shelf.example`, operator)
  const peek = ivy.db('main').collection('pins').subscribe({})
  await peek.ready.catch(() => undefined)
  await main(halT).fn('pin', { org: team.id, title: 'Not for strangers' })
  check('and the members\' own view carries on', await until(() => live.items.some((p) => p.title === 'Not for strangers')))
  check('a stranger following the team sees nothing of it: the read rule shows them nothing', peek.items.length === 0, JSON.stringify(peek.items))
  peek.close()

  // Hal follows too; when they are removed from the team their list resets and empties.
  const halLive = halT.db('main').collection('pins').subscribe({ jitterMs: 50 })
  await halLive.ready
  await main(gusT).fn('pin', { org: team.id, title: 'Before the removal' })
  check('the other member sees it too', await until(() => halLive.items.length === 2))
  /** @type {string[]} */
  const resets = []
  halLive.on('reset', (e) => resets.push(e.reason))
  let lost = false
  halLive.on('access_lost', () => (lost = true))
  await gusT.orgs.members(team.id).remove(halId)
  check('a removed member\'s list resets at once (access_changed)', await until(() => resets.includes('access_changed')), JSON.stringify(resets))
  check('and loses what it showed', await until(() => halLive.items.length === 0))
  await main(gusT).fn('pin', { org: team.id, title: 'After the removal' })
  await new Promise((r) => setTimeout(r, 500))
  check('nothing more reaches them', halLive.items.length === 0)
  live.close()
  halLive.close()

  // Revoking the session ends the stream.
  const jo = await signedIn(`jo-${STAMP}@shelf.example`, operator)
  const joInvite = await gusT.orgs.invitations(team.id).create({ binding: 'none', roles: ['editor'], maxUses: 1 })
  const joT = await joinedTeam(jo, team.slug, joInvite.token)
  const joLive = joT.db('main').collection('pins').subscribe({})
  await joLive.ready
  let ended = null
  joLive.on('error', (e) => (ended = e))
  await joT.auth.logout()
  check('signing out ends the stream (session_ended)', await until(() => ended !== null) && /** @type {any} */ (ended).code === 'session_ended', String(ended))
}

heading('Live gallery (ch24)')
{
  const STAMP = Date.now().toString(36)
  const tag = `live-${STAMP}`
  const until = async (/** @type {() => boolean} */ cond, /** @type {number} */ ms = 15000) => {
    for (const end = Date.now() + ms; Date.now() < end; ) {
      if (cond()) return true
      await new Promise((r) => setTimeout(r, 50))
    }
    return false
  }
  const mineAssets = main(member).collection('assets')
  // What the app follows: the published assets, here narrowed to this run's tag.
  const live = mineAssets.subscribe({ where: { published_at: { $ne: null }, tags: tag }, load: { sort: '-published_at', limit: 50 } })
  const state = await live.ready
  check('the gallery loads, then follows (status live)', state.status === 'live' && state.items.length === 0, state.status)
  const shown = await mineAssets.create({ title: `Live ${STAMP}`, kind: 'note', tags: [tag] })
  const other = await mineAssets.create({ title: `Elsewhere ${STAMP}`, kind: 'note', tags: [`${tag}-other`] })
  const draftOnly = await mineAssets.create({ title: `Still a draft ${STAMP}`, kind: 'note', tags: [tag] })
  await new Promise((r) => setTimeout(r, 500))
  check('a draft is not in the gallery', !live.items.some((a) => a.id === draftOnly.id))
  await main(curator).fn('publish', { asset_id: shown.id }, { idempotencyKey: `publish-${shown.id}` })
  await main(curator).fn('publish', { asset_id: other.id }, { idempotencyKey: `publish-${other.id}` })
  check("a curator's publish appears without a reload", await until(() => live.items.some((a) => a.id === shown.id)))
  check('an asset outside the filter (another tag) does not', !live.items.some((a) => a.id === other.id))
  const current = await mineAssets.get(shown.id)
  await mineAssets.patch(shown.id, { title: `Live ${STAMP} (edited)` }, { ifMatch: current._meta.version })
  check('an edit reaches the card', await until(() => live.items.some((a) => a.id === shown.id && a.title.endsWith('(edited)'))))
  await mineAssets.delete(shown.id)
  check('a delete removes it', await until(() => live.items.length === 0))
  live.close()
  await mineAssets.delete(other.id)
  await mineAssets.delete(draftOnly.id)
  // A search can't be followed: the app shows it as a plain list until it is cleared.
  await refused('a search is not a live filter (400 invalid_query)', () => mineAssets.subscribe({ where: { $search: 'live' } }).ready, ValidationError, 'invalid_query')
}

heading('Private notes (ch25)')
{
  const STAMP = Date.now().toString(36)
  const SECRET = `the vendor's price is ${STAMP}`
  const ben = await signedIn(`ben25-${STAMP}@shelf.example`, operator)
  const cy = await signedIn(`cy25-${STAMP}@shelf.example`, operator)
  const eve = await signedIn(`eve25-${STAMP}@shelf.example`, operator)
  const dee = await signedIn(`dee25-${STAMP}@shelf.example`, operator)
  const deeId = (await dee.auth.me()).id
  const pinsOf = (/** @type {any} */ c) => c.db('main').collection('pins')
  const hidden = (/** @type {any} */ p) => p.private_notes === null && p._meta.redacted?.includes('private_notes')

  // The key: created and saved (here: by the stack's start), listed with its fingerprint and the field using it.
  const keyInfo = (await operator.admin.secrets.list()).find((k) => k.name === 'SHELF_NOTES')
  check('SHELF_NOTES is a key with a fingerprint, used by the pins\' private notes', keyInfo?.type === 'key' && /^[0-9a-f]{8}$/.test(keyInfo.fingerprint ?? '') && (keyInfo.used_by ?? []).includes('main/pins.private_notes'), JSON.stringify(keyInfo))
  await refused('a plain secret write can\'t replace a key (409)', () => operator.admin.secrets.set('SHELF_NOTES', 'x'), ConflictError, 'conflict')

  // A team: Ben owns it, Cy edits, Eve is a plain member, Dee is an admin.
  const team = await ben.orgs.create({ name: `Notes ${STAMP}`, slug: `notes-${STAMP}` })
  const benT = ben.org(team.slug)
  /** @type {any[]} */
  const joined = []
  for (const [who, roles] of /** @type {const} */ ([[cy, ['editor']], [eve, []], [dee, []]])) {
    const link = await benT.orgs.invitations(team.id).create({ binding: 'none', roles, maxUses: 1 })
    joined.push(await joinedTeam(who, team.slug, link.token))
  }
  const [cyT, eveT, deeT] = joined
  await benT.orgs.members(team.id).update(deeId, ['admin'])

  // The author writes private notes; who may read them is decided by `reveal`.
  const made = /** @type {any} */ (await main(cyT).fn('pin', { org: team.id, title: 'Quote', private_notes: SECRET }))
  const seenBy = async (/** @type {any} */ c) => (await pinsOf(c).list()).items.find((p) => p.id === made.id)
  check('the author reads the private notes', (await seenBy(cyT)).private_notes === SECRET)
  check('the team\'s owner reads them', (await seenBy(benT)).private_notes === SECRET)
  check('so does an admin', (await seenBy(deeT)).private_notes === SECRET)
  const eves = await seenBy(eveT)
  check('a plain member reads the pin, with the notes null and listed in _meta.redacted', eves.title === 'Quote' && hidden(eves), JSON.stringify(eves))
  const etag = (await fetch(`${url}/v1/shelf/main/pins/${made.id}`, { headers: { Authorization: `Bearer ${await eveT.storage.get()}` } })).headers.get('etag') ?? ''
  check('what is hidden is part of the ETag, so a change of view never gets a stale 304', /\.r[0-9a-f]{8}"$/.test(etag), etag)
  check('a team search by the note finds nothing (an encrypted field can\'t be queried)', await pinsOf(cyT).list({ where: { private_notes: SECRET } }).then(() => false, (e) => e.code === 'field_encrypted'))

  // The database holds ciphertext.
  const raw = execFileSync('docker', [...composeArgs, 'exec', '-T', 'mongo', 'mongosh', '--quiet', '--eval', `EJSON.stringify(db.getSiblingDB('shelf__main').pins.findOne({_id: '${made.id}'}))`]).toString()
  check('MongoDB holds binary of subtype 6 where the notes are', raw.includes('"subType":"06"') && !raw.includes(SECRET) && raw.includes('Quote'), raw.slice(0, 300))

  // Rotating the key is instant: nothing is rewritten, everything still reads, and the new key is shown once.
  const version = (await seenBy(cyT))._meta.version
  await operatorProof()
  const rotated = /** @type {any} */ (await operator.admin.secrets.rotateKey('SHELF_NOTES'))
  check('rotating returns the new key once, with both fingerprints, and wraps the data keys again', typeof rotated.key === 'string' && rotated.key.length === 43 && rotated.fingerprint !== rotated.previous_fingerprint && rotated.rewrapped >= 1, JSON.stringify({ ...rotated, key: '…' }))
  const after = await seenBy(cyT)
  check('after the rotation the notes read the same and no pin was rewritten', after.private_notes === SECRET && after._meta.version === version)

  // An export: ciphertext unless you hold the key, and the key opens only what it protects.
  const dir = `/tmp/shelf-pins-${STAMP}`
  const mine = mkdtempSync(join(tmpdir(), 'shelf-export-'))
  const exportPins = (/** @type {string} */ out, /** @type {string[]} */ extra = []) =>
    inStack(['export', '--direct', '--realm', 'shelf', '--db', 'main', '--collection', 'pins', '--no-files', '--out', out, ...extra])
  const pinsOut = (/** @type {string} */ out, /** @type {string} */ name) => {
    execFileSync('docker', [...composeArgs, 'cp', `backd:${out}/main/pins.jsonl`, join(mine, name)], { stdio: 'ignore' })
    return readFileSync(join(mine, name), 'utf8')
  }
  exportPins(dir)
  const closed = pinsOut(dir, 'closed.jsonl')
  check('an export without the key holds ciphertext and the title', closed.includes('"subType":"06"') && !closed.includes(SECRET) && closed.includes('Quote'))
  exportPins(`${dir}-open`, ['--field', `private_notes=key:${rotated.key}`])
  const open = pinsOut(`${dir}-open`, 'open.jsonl')
  check('an export with the key opens the notes', open.includes(SECRET))
  let wrong = false
  try {
    exportPins(`${dir}-wrong`, ['--field', `private_notes=key:${'A'.repeat(43)}`])
  } catch {
    wrong = true
  }
  check('a wrong key is refused, by its fingerprint', wrong)
}

heading('Your data (ch26)')
{
  const STAMP = Date.now().toString(36)
  const SECRET = `my private note ${STAMP}`
  const listed = await anonymous.consents.current()
  const privacy = listed.find((c) => c.name === 'privacy')
  check('the realm also asks for the privacy notice (required), with a public text', privacy?.required === true && (await anonymous.consents.text('privacy', privacy.current.version)).text.includes('privacy notice'), JSON.stringify(privacy))
  const invite = await operator.admin.invitations.create({ email: `only-terms-${STAMP}@shelf.example` })
  await refused('a sign-up with the terms but not the privacy notice is refused (400 consent_required)', () => createClient({ url, realm: 'shelf' }).auth.signUpWithOrg({ email: `only-terms-${STAMP}@shelf.example`, password: PASSWORD, name: 'Only terms', slug: `only-terms-${STAMP}`, invitation: invite.token, consents: { terms: consentVersions.terms } }), ConsentRequiredError, 'consent_required')

  // A member with something of their own: a pin in a team, with a private note (chapter 25).
  const ana = await signedIn(`ana26-${STAMP}@shelf.example`, operator)
  const anaEmail = (await ana.auth.me()).email
  const team = await ana.orgs.create({ name: `Mine ${STAMP}`, slug: `mine-${STAMP}` })
  await main(ana.org(team.slug)).fn('pin', { org: team.id, title: 'Quote', private_notes: SECRET })

  await refused('asking for a copy needs a fresh proof (401 reauth_required)', () => ana.account.requestExport(), AuthenticationError, 'reauth_required')
  await ana.auth.reauth({ method: 'password', password: PASSWORD })
  const asked = await ana.account.requestExport()
  check('the request is queued', asked.status === 'queued' || asked.status === 'running', JSON.stringify(asked))
  await refused('a second request in the same day is refused (429)', () => ana.account.requestExport(), RetryableError, 'too_many_requests')

  const ready = await ana.account.waitForExport(asked.id, { pollIntervalMs: 500, timeoutMs: 120000 })
  check('the worker builds it, and says what it holds', ready.status === 'ready' && ready.size > 0 && ready.counts?.['_account/account'] === 1 && ready.counts?.['main/pins.jsonl'] >= 1, JSON.stringify(ready))
  check('it is listed, newest first', (await ana.account.exports())[0].id === asked.id)

  const zip = readZip(await (await ana.account.downloadExport(asked.id)).bytes())
  const manifest = JSON.parse(await zip.text('manifest.json'))
  check('the package is a partial, personal export of this user, with checksums', manifest.flags.personal === true && manifest.flags.partial === true && manifest.subject.startsWith('user:') && Object.keys(manifest.checksums).length >= zip.names.length - 1, JSON.stringify(manifest.flags))
  const readme = await zip.text('README.html')
  check('the README names the member and lists the system data', readme.includes(anaEmail) && readme.includes('_account') && readme.includes('In Shelf'))
  const pins = await zip.text('main/pins.jsonl')
  check('the pin is in it, and its private note is in the clear (the server opens it for its owner)', pins.includes('Quote') && pins.includes(SECRET))
  const account = await zip.text('_account/account.jsonl')
  check('the account is in it; a password hash is not', account.includes(anaEmail) && !zip.names.some((n) => n.includes('password')) && !(await Promise.all(zip.names.map((n) => zip.text(n)))).some((t) => t.includes('$argon2')))

  // It is theirs alone.
  await refused('another member can neither read it (404)', () => member.account.export(asked.id), NotFoundError, 'not_found')
  await refused('nor download it (404)', () => member.account.downloadExport(asked.id), NotFoundError, 'not_found')

  // The email links the app's page, never the file.
  /** @type {any} */
  let mail
  for (let i = 0; i < 30 && !mail; i++) { // the worker delivers the queued mail a moment later
    mail = (await operator.db('mail').collection('outbox').list({ where: { kind: 'data-export-ready', to: anaEmail }, orderBy: '-_meta.created_at', limit: 1 })).items[0]
    if (!mail) await new Promise((r) => setTimeout(r, 500))
  }
  check('the member was emailed a link to the app\'s page with the export id', mail?.link === `http://localhost:8080/?export=${asked.id}` && !mail.text.includes('.zip'), JSON.stringify(mail?.link))

  // What support does for a formal request goes through the admin API, with the same package.
  await operatorProof()
  const forUser = await fetch(`${url}/v1/shelf/_admin/users/${(await ana.auth.me()).id}/export`, {
    method: 'POST', headers: { Authorization: `Bearer ${await operator.storage.get()}`, 'X-Backd-Reauth': (operator.proof?.token ?? ''), 'Content-Type': 'application/json' },
    body: JSON.stringify({ ticket: `REQ-${STAMP}`, no_notify: true, reason: 'the tour checks silent exports' }),
  })
  const made = /** @type {any} */ (await forUser.json())
  check('an administrator can export a user\'s data without telling them, with the reason kept', forUser.status === 202 && made.silent === true && made.requested_by !== 'user', JSON.stringify(made))
}

heading('cleanup by hand (ch9)')
const sweep = /** @type {any} */ (await operator.admin.invokeFunction('main/cleanup'))
const swept = /** @type {any} */ (await sweep.wait({ pollIntervalMs: 300, timeoutMs: 90000 }))
check('cleanup runs and reports', typeof swept.shares === 'number' && typeof swept.notifications === 'number', JSON.stringify(swept))

console.log(`\n${failed === 0 ? 'All checks passed.' : `${failed} check(s) failed.`}`)
process.exit(failed === 0 ? 0 : 1)
