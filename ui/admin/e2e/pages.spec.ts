import { expect, test } from './fixtures'
import { REALM } from './users'
import { go } from './nav'

// The pages of the sidebar that no other spec opens: overview, security, compliance, app config,
// and the sections of the configuration. Each loads, says what it shows, and offers no way to change
// the configuration (it is read from files and never written here).

test('the overview shows the realm at a glance, what needs attention and what the level may do', async ({ page, signIn }) => {
  await signIn('admin')
  await expect(page.getByRole('heading', { name: 'Overview' })).toBeVisible()
  await expect(page.getByTestId('stat-collections')).toContainText(/\d/)
  await expect(page.getByTestId('stat-functions')).toContainText(/\d/)
  await expect(page.getByTestId('attention')).toContainText('signup: open')
  await expect(page.getByTestId('level')).toHaveText('Full administrator')
  await expect(page.getByTestId('can-read')).toContainText('Data')
  await expect(page.getByTestId('can-write')).toContainText('Users')
})

test('a read-only administrator reads on the overview and changes nothing', async ({ page, signIn }) => {
  await signIn('viewer')
  await expect(page.getByTestId('level')).toHaveText('Read-only')
  await expect(page.getByTestId('can-write')).toHaveText('Nothing')
})

test('security shows the passwords and the second-factor coverage, and a read-only level has no actions', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Security')
  await expect(page.getByRole('heading', { name: 'Security', level: 1 })).toBeVisible()
  await expect(page.getByTestId('passwords-status')).toContainText('accounts have a password')
  await expect(page.getByTestId('mfa-coverage')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Expire every password' })).toBeVisible()
  await page.getByTestId('account-menu').click()
  await page.getByTestId('sign-out').click()

  await signIn('viewer')
  await go(page, 'Security')
  await expect(page.getByRole('heading', { name: 'Security', level: 1 })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Expire every password' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'End every session' })).toHaveCount(0)
})

test('compliance lists what a user\'s export covers for every collection', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Compliance')
  await expect(page.getByRole('heading', { name: 'Compliance', level: 1 })).toBeVisible()
  const table = page.getByTestId('scope-table')
  await expect(table).toContainText('main/docs')
  await expect(table).toContainText('main/projects')
  await expect(table).toContainText('documents the user owns')
})

test('app config says so when the realm declares none', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'App config')
  await expect(page.getByRole('heading', { name: 'App config', level: 1 })).toBeVisible()
  await expect(page.getByText('This realm declares no app configuration.')).toBeVisible()
})

test('a page that does not exist says so and leads back', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/no-such-page`)
  await expect(page.getByRole('link', { name: 'Back to the start' })).toBeVisible()
  await page.getByRole('link', { name: 'Back to the start' }).click()
  await expect(page).toHaveURL(/\/_ui\/$/)
})

test.describe('the sections of the configuration', () => {
  test.beforeEach(async ({ page, signIn }) => {
    await signIn('admin')
    await go(page, 'Configuration')
  })

  test('sign-in is drawn as a map of who arrives and by which ways', async ({ page }) => {
    await page.getByRole('link', { name: 'Sign-in', exact: true }).click()
    await expect(page.getByTestId('signin-map')).toBeVisible()
    await expect(page.getByTestId('registration')).toHaveText('Open to anyone')
    await expect(page.getByTestId('strategy-password')).toBeVisible()
    await expect(page.getByTestId('strategy-passkey')).toBeVisible()
  })

  test('files lists every file the server loaded, and shows one read-only', async ({ page }) => {
    await page.getByRole('link', { name: 'Files', exact: true }).click()
    await expect(page.getByTestId('file-tree')).toBeVisible()
    await expect(page.getByTestId('fn-file-name')).toContainText('realm.yaml')
    await expect(page.getByTestId('fn-source')).toContainText('strategies')
    await expect(page.getByRole('button', { name: /^(Save|Edit|Apply)/ })).toHaveCount(0)
  })

  test('backup gives the commands to run, and runs nothing', async ({ page }) => {
    await page.getByRole('link', { name: 'Backup', exact: true }).click()
    await expect(page.getByTestId('cmd-export')).toContainText(`backd export --realm ${REALM}`)
    await expect(page.getByTestId('cmd-import')).toContainText(`--realm ${REALM}`)
    await expect(page.getByRole('button', { name: /^(Export|Import|Run)/ })).toHaveCount(0)
  })

  test('API files offer the realm\'s OpenAPI documents and generated types for download', async ({ page }) => {
    await page.getByRole('link', { name: 'API files', exact: true }).click()
    await expect(page.getByText('Client API of this realm (OpenAPI)')).toBeVisible()
    await expect(page.getByTestId('file-client')).toBeVisible()
    await expect(page.getByText(`/v1/${REALM}/_admin/openapi.json`)).toBeVisible()
    await expect(page.getByTestId('file-admin')).toBeVisible()
    const download = page.waitForEvent('download')
    await page.getByTestId('file-types').click()
    expect((await download).suggestedFilename()).toMatch(/\.d\.ts$|\.ts$/)
  })
})
