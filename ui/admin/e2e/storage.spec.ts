import { createClient } from 'backd-js'
import { expect, test } from './fixtures'
import { PASSWORD, REALM, users } from './users'

// The storage page reads the realm's usage totals and quotas (storage.quota in the realm's realm.yaml).
const storage = 'http://localhost:9000'
test.use({ allowedOrigins: [storage] })

const base = process.env.E2E_URL ?? 'http://localhost:8080'
const stamp = Date.now().toString(36)

// A 1×1 PNG.
const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==', 'base64')

test('the storage page shows what is in use against the quotas, and who holds it', async ({ page, signIn }) => {
  const client = createClient({ url: base, realm: REALM })
  await client.auth.login({ email: users.admin, password: PASSWORD })
  const doc = await client.admin.data('main', 'docs').create({ title: `Storage ${stamp}` })

  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/docs/${doc.id}`)
  const cover = page.getByTestId('files-cover')
  await cover.getByTestId('upload-cover').setInputFiles({ name: 'dot.png', mimeType: 'image/png', buffer: PNG })
  await expect(cover.getByTestId('file-name')).toHaveText('dot.png')

  await page.getByRole('link', { name: 'Storage' }).click()
  await expect(page.getByRole('heading', { name: 'Storage', level: 1 })).toBeVisible()
  await expect(page.getByTestId('storage-where')).toContainText('access keys: ok')

  await expect(page.getByTestId('storage-usage')).toContainText("of the realm's quota of 10.0 MiB")
  await expect(page.getByTestId('realm-meter')).toBeVisible()
  await expect(page.getByTestId('user-quota')).toContainText('Quota per user: 5.0 MiB')
  await expect(page.getByTestId('storage-usage')).toContainText(/in [1-9]\d* files/) // documents made by an administrator have no owner: only the realm counts them
  await expect(page.getByTestId('storage-near')).toHaveCount(0) // far from both limits
  await expect(page.getByTestId('storage-queue')).toContainText('objects wait to be deleted')
})

test('a read-only administrator sees the storage too', async ({ page, signIn }) => {
  await signIn('viewer', { keep: true })
  await page.getByRole('link', { name: 'Storage' }).click()
  await expect(page.getByTestId('storage-usage')).toBeVisible()
})
