import { createClient } from 'backd-js'
import { expect, test } from './fixtures'
import { PASSWORD, REALM, users } from './users'

const url = process.env.E2E_URL ?? 'http://localhost:8080'

/** The support user's phone, registered the way an app would. */
async function registerDevice(name: string) {
  const client = createClient({ url, realm: REALM })
  await client.auth.login({ email: users.support, password: PASSWORD })
  const device = await client.push.register({ platform: 'android', token: `test-e2e-${Date.now().toString(36)}`, name })
  await client.auth.logout().catch(() => undefined)
  return device
}

test('an administrator sees a user\'s devices and removes one', async ({ page, signIn }) => {
  const label = `E2E phone ${Date.now()}`
  await registerDevice(label)
  await signIn('admin')
  await page.getByRole('link', { name: 'Users' }).click()
  await page.getByRole('link', { name: users.support }).click()
  const row = page.getByTestId('user-devices').getByRole('row').filter({ hasText: label })
  await expect(row).toBeVisible()
  await expect(row).toContainText('android')
  await row.getByRole('button', { name: 'Remove' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Device removed' })).toBeVisible()
  await expect(page.getByTestId('user-devices').getByRole('row').filter({ hasText: label })).toHaveCount(0)
})

test('the Push view shows the configuration, sends a test and lists the send', async ({ page, signIn }) => {
  await registerDevice(`E2E phone ${Date.now()}`)
  await signIn('admin')
  await page.getByRole('link', { name: 'Push', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Push notifications' })).toBeVisible()
  await expect(page.getByTestId('push-config')).toContainText('android')
  await expect(page.getByTestId('push-config')).toContainText('capture')
  await expect(page.getByTestId('push-missing')).toHaveCount(0)

  await page.getByRole('button', { name: 'Send a test' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel("User's email").fill(users.support)
  await dialog.getByLabel('Title').fill('E2E test')
  await dialog.getByLabel('Body').fill('Does it ring?')
  await dialog.getByRole('button', { name: 'Send', exact: true }).click()
  await expect(dialog.getByTestId('push-test-result')).toContainText('queued')
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  // The send is in the list, and opens with its counts and no content.
  const row = page.getByTestId('push-sends').getByRole('row').filter({ hasText: 'raw' }).first()
  await expect(row).toBeVisible()
  await row.click()
  await expect(page.getByTestId('push-send')).toContainText('admin (')
  await expect(page.getByTestId('push-send')).not.toContainText('Does it ring?')
})

test('a read-only administrator reads the Push view but can not send', async ({ page, signIn }) => {
  await signIn('viewer')
  await page.getByRole('link', { name: 'Push', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Push notifications' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Send a test' })).toHaveCount(0)
})
