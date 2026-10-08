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
import { deflateSync } from 'node:zlib'
import { createClient, AuthenticationError, ForbiddenError, NotFoundError, VersionMismatchError, versionStatus } from '../../src/index.js'

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

heading('cleanup by hand (ch9)')
const sweep = /** @type {any} */ (await operator.admin.invokeFunction('main/cleanup'))
const swept = /** @type {any} */ (await sweep.wait({ pollIntervalMs: 300, timeoutMs: 90000 }))
check('cleanup runs and reports', typeof swept.shares === 'number' && typeof swept.notifications === 'number', JSON.stringify(swept))

console.log(`\n${failed === 0 ? 'All checks passed.' : `${failed} check(s) failed.`}`)
process.exit(failed === 0 ? 0 : 1)
