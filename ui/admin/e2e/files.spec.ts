import { createClient } from 'backd-js'
import { expect, test } from './fixtures'
import { PASSWORD, REALM, users } from './users'

// The file storage is another origin (the local stack's MinIO): previews load from it,
// and a download navigates to it.
const storage = 'http://localhost:9000'
test.use({ allowedOrigins: [storage] })

const base = process.env.E2E_URL ?? 'http://localhost:8080'
const stamp = Date.now().toString(36)
let n = 0
const title = () => `Files ${stamp} ${++n}`

// A 1×1 PNG.
const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64')

async function newDoc() {
  const client = createClient({ url: base, realm: REALM })
  await client.auth.login({ email: users.admin, password: PASSWORD })
  const doc = await client.admin.data('main', 'docs').create({ title: title() })
  return { client, doc, col: client.admin.data('main', 'docs') }
}

test('a picture is uploaded, previewed, downloaded and removed', async ({ page, signIn }) => {
  const { doc } = await newDoc()
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/docs/${doc.id}`)
  const cover = page.getByTestId('files-cover')
  await expect(cover).toContainText('One file of up to 1.0 MiB')
  await expect(cover).toContainText('image/png, image/jpeg')
  await expect(cover).toContainText('No file.')

  await cover.getByTestId('upload-cover').setInputFiles({ name: 'dot.png', mimeType: 'image/png', buffer: PNG })
  await expect(cover.getByTestId('file-name')).toHaveText('dot.png')
  await expect(cover).toContainText('image/png · 70 B')
  await expect(cover.getByRole('img', { name: 'Preview of dot.png' })).toBeVisible()
  await expect(page.getByTestId('doc-version')).toHaveText('2') // an upload is a change to the document
  await expect(cover.getByTestId('upload-cover')).toBeVisible() // a single field still takes a replacement

  // Download: the link forces a save.
  const download = page.waitForEvent('download')
  await cover.getByRole('button', { name: 'Download dot.png' }).click()
  const saved = await download
  expect(saved.suggestedFilename()).toBe('dot.png')
  const path = await saved.path()
  expect((await import('node:fs')).readFileSync(path!).equals(PNG)).toBe(true)

  // Remove asks for the file's name.
  await cover.getByRole('button', { name: 'Remove dot.png' }).click()
  await page.getByLabel('Type dot.png to confirm').fill('dot.png')
  await page.getByRole('button', { name: 'Remove file' }).click()
  await expect(cover).toContainText('No file.')
  await expect(page.getByTestId('doc-version')).toHaveText('3')
})

test('a direct upload shows its progress, and the field is full at max_files', async ({ page, signIn }) => {
  const { doc } = await newDoc()
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/docs/${doc.id}`)
  const manuals = page.getByTestId('files-manuals')
  await expect(manuals).toContainText('Up to 2 files of 2.0 MiB each')
  await expect(manuals).toContainText('Sent straight to the storage')
  for (const name of ['a.txt', 'b.txt']) {
    await manuals.getByTestId('upload-manuals').setInputFiles({ name, mimeType: 'text/plain', buffer: Buffer.from(`manual ${name}`) })
    await expect(manuals.getByTestId('file-name').filter({ hasText: name })).toBeVisible()
  }
  await expect(manuals.getByTestId('file')).toHaveCount(2)
  await expect(manuals.getByTestId('upload-manuals')).toHaveCount(0) // two of two: no more to add
  await expect(page.getByTestId('doc-version')).toHaveText('3')
})

test('what the field refuses is said, and nothing is stored', async ({ page, signIn }) => {
  const { doc, col } = await newDoc()
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/docs/${doc.id}`)
  const cover = page.getByTestId('files-cover')
  // Over 1 MiB, and a type the field doesn't take (by what the bytes say).
  await cover.getByTestId('upload-cover').setInputFiles({ name: 'big.png', mimeType: 'image/png', buffer: Buffer.concat([PNG, Buffer.alloc(1100 * 1024)]) })
  await expect(page.getByRole('alert').filter({ hasText: /exceeds/ }).first()).toBeVisible()
  await cover.getByTestId('upload-cover').setInputFiles({ name: 'fake.png', mimeType: 'image/png', buffer: Buffer.from('plain text pretending to be a picture') })
  await expect(page.getByRole('alert').filter({ hasText: /doesn't accept/ }).first()).toBeVisible()
  await expect(cover).toContainText('No file.')
  expect((await col.get(doc.id))._meta.version).toBe(1)
})

test('unsaved edits survive a file change', async ({ page, signIn }) => {
  const { doc } = await newDoc()
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/docs/${doc.id}`)
  await page.getByLabel('title').fill('Edited but not saved')
  await page.getByTestId('upload-cover').setInputFiles({ name: 'dot.png', mimeType: 'image/png', buffer: PNG })
  await expect(page.getByTestId('file-name')).toHaveText('dot.png')
  await expect(page.getByLabel('title')).toHaveValue('Edited but not saved')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByTestId('doc-version')).toHaveText('3') // saved on top of the new version, no conflict
  await expect(page.getByTestId('file-name')).toHaveText('dot.png') // and the PUT kept the file
})

test('a read-only administrator downloads but changes nothing', async ({ page, signIn }) => {
  const { doc, col } = await newDoc()
  await col.uploadFile(doc.id, 'cover', new Blob([PNG], { type: 'image/png' }), { name: 'dot.png' })
  await signIn('viewer', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/docs/${doc.id}`)
  const cover = page.getByTestId('files-cover')
  await expect(cover.getByTestId('file-name')).toHaveText('dot.png')
  await expect(cover.getByRole('button', { name: 'Download dot.png' })).toBeVisible()
  await expect(cover.getByRole('button', { name: 'Remove dot.png' })).toHaveCount(0)
  await expect(cover.getByTestId('upload-cover')).toHaveCount(0)
  await expect(page.getByTestId('files')).toContainText('Files can be changed by an administrator with write access')
})
