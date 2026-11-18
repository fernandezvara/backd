import { expect, test } from './fixtures'
import { REALM, users } from './users'
import { go, navLabels, setTheme } from './nav'

test('the overview shows the realm, its fingerprint and the startup warnings', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Configuration')
  await expect(page.getByTestId('config-realm')).toHaveText(REALM)
  // The page shows the end of the fingerprint; the whole one is copied.
  await expect(page.getByTestId('config-fingerprint')).toHaveText(/^[0-9a-f]{8}$/)
  await expect(page.getByTestId('config-warnings')).toContainText('admin_networks_open')
  await expect(page.getByTestId('config-warnings')).toContainText('signup_open')
})

test('settings and roles are listed, with the seeded users', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Configuration')
  await page.getByRole('link', { name: 'Settings' }).click()
  const settings = page.getByTestId('settings-sections')
  await expect(settings.getByRole('row', { name: /^signup open/ })).toBeVisible()
  await expect(settings.getByRole('row', { name: /admin\.read_access\.users true/ })).toBeVisible()
  const roles = page.getByTestId('roles-table')
  await expect(roles.getByRole('row', { name: /admin/ }).first()).toContainText(users.admin)
  await expect(roles.getByRole('row', { name: /viewer/ })).toContainText('read')
  await expect(roles.getByRole('row', { name: /support/ })).toContainText('users, invitations')
})

// The collections are read on their own page, in the Data area: its Schema and Rules tabs show the files.
test('collections show schema, indexes, rules and policy with their files', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Data')
  await page.getByTestId('open-main-notes').click()
  await page.getByTestId('tab-schema').click()
  await expect(page.getByTestId('schema-fields')).toContainText('title')
  await expect(page.getByTestId('schema-fields')).toContainText('boolean')
  await expect(page.locator('main')).toContainText('pinned,-_meta.created_at')
  await expect(page.locator('main')).toContainText('adminui/main/notes/schema.json')
  await page.getByTestId('tab-rules').click()
  await expect(page.locator('main')).toContainText('user != nil && document._meta.owner == user.id')
  await expect(page.locator('main')).toContainText('adminui/main/notes/collection.yaml')
})

test('templates say so when there are none', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Configuration')
  await page.getByRole('link', { name: 'Templates' }).click()
  await expect(page.getByText('No email templates.')).toBeVisible()
  await expect(page.getByText('No hosted pages.')).toBeVisible()
})

test('a read-only administrator reads the configuration', async ({ page, signIn }) => {
  await signIn('viewer')
  await go(page, 'Configuration')
  await expect(page.getByTestId('config-realm')).toHaveText(REALM)
  await expect(page.getByRole('button', { name: /^(Save|Edit|Delete)/ })).toHaveCount(0)
  await go(page, 'Data')
  await page.getByTestId('open-main-notes').click()
  await page.getByTestId('tab-rules').click()
  await expect(page.getByRole('button', { name: /^(Save|Edit|Delete)/ })).toHaveCount(0)
})

test('a custom level without the config area has no way in', async ({ page, signIn }) => {
  await signIn('support')
  expect(await navLabels(page)).toContain('Users')
  expect(await navLabels(page)).not.toContain('Configuration')
})

test('the configuration views have no accessibility violations', async ({ page, signIn }) => {
  const { default: AxeBuilder } = await import('@axe-core/playwright')
  await signIn('admin')
  for (const theme of ['Light', 'Dark']) {
    await setTheme(page, theme)
    await go(page, 'Configuration')
    await expect(page.getByTestId('config-realm')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    for (const tab of ['Settings', 'Sign-in', 'Templates', 'Files', 'Backup', 'API files']) {
      await page.getByRole('link', { name: tab, exact: true }).click()
      await page.getByRole('heading', { level: 2 }).first().waitFor()
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    }
  }
})
