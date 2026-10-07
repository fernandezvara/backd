import { expect, test } from './fixtures'
import { REALM, users } from './users'

test('the overview shows the realm, its fingerprint and the startup warnings', async ({ page, signIn }) => {
  await signIn('admin')
  await page.getByRole('link', { name: 'Configuration' }).click()
  await expect(page.getByTestId('config-realm')).toHaveText(REALM)
  await expect(page.getByTestId('config-fingerprint')).toHaveText(/^[0-9a-f]{64}$/)
  await expect(page.getByTestId('config-warnings')).toContainText('admin_networks_open')
  await expect(page.getByTestId('config-warnings')).toContainText('signup_open')
})

test('settings and roles are listed, with the seeded users', async ({ page, signIn }) => {
  await signIn('admin')
  await page.getByRole('link', { name: 'Configuration' }).click()
  await page.getByRole('link', { name: 'Settings' }).click()
  const settings = page.getByTestId('settings-table')
  await expect(settings.getByRole('row', { name: /^signup open/ })).toBeVisible()
  await expect(settings.getByRole('row', { name: /admin\.read_access\.users true/ })).toBeVisible()
  const roles = page.getByTestId('roles-table')
  await expect(roles.getByRole('row', { name: /admin/ }).first()).toContainText(users.admin)
  await expect(roles.getByRole('row', { name: /viewer/ })).toContainText('read')
  await expect(roles.getByRole('row', { name: /support/ })).toContainText('users, invitations')
})

test('collections show schema, indexes, rules and policy with their files', async ({ page, signIn }) => {
  await signIn('admin')
  await page.getByRole('link', { name: 'Configuration' }).click()
  await page.getByRole('link', { name: 'Collections' }).click()
  const notes = page.getByTestId('col-main-notes')
  await notes.getByText('notes', { exact: true }).click()
  await expect(notes).toContainText('adminui/main/notes/schema.json')
  await expect(notes.getByRole('region', { name: /Schema/ })).toContainText('"minLength": 1')
  await expect(notes).toContainText('pinned,-_meta.created_at')
  await expect(notes).toContainText('user != nil && document._meta.owner == user.id')
  await expect(notes).toContainText('adminui/main/notes/rules.yaml')
  await expect(notes).toContainText('Soft delete, kept for 30d')
  await expect(notes).toContainText('When an owner is erased: delete')
})

test('templates say so when there are none', async ({ page, signIn }) => {
  await signIn('admin')
  await page.getByRole('link', { name: 'Configuration' }).click()
  await page.getByRole('link', { name: 'Templates' }).click()
  await expect(page.getByText('No email templates.')).toBeVisible()
  await expect(page.getByText('No hosted pages.')).toBeVisible()
})

test('a read-only administrator reads the configuration', async ({ page, signIn }) => {
  await signIn('viewer')
  await page.getByRole('link', { name: 'Configuration' }).click()
  await expect(page.getByTestId('config-realm')).toHaveText(REALM)
  await page.getByRole('link', { name: 'Collections' }).click()
  await expect(page.getByTestId('col-main-notes')).toBeVisible()
  await expect(page.getByRole('button', { name: /^(Save|Edit|Delete)/ })).toHaveCount(0)
})

test('a custom level without the config area has no way in', async ({ page, signIn }) => {
  await signIn('support')
  await expect(page.getByRole('link', { name: 'Users' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Configuration' })).toHaveCount(0)
})

test('the configuration views have no accessibility violations', async ({ page, signIn }) => {
  const { default: AxeBuilder } = await import('@axe-core/playwright')
  await signIn('admin')
  for (const theme of ['Light', 'Dark']) {
    await page.getByLabel('Theme').selectOption({ label: theme })
    await page.getByRole('link', { name: 'Configuration' }).click()
    await expect(page.getByTestId('config-realm')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    for (const tab of ['Settings', 'Collections', 'Templates']) {
      await page.getByRole('link', { name: tab, exact: true }).click()
      await page.getByRole('heading', { level: 2 }).first().waitFor()
      if (tab === 'Collections') await page.getByTestId('col-main-notes').getByText('notes', { exact: true }).click()
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    }
  }
})

test('a change to a schema or to rules is prepared as a file to download, and nothing is sent to the server', async ({ page, signIn }) => {
  await signIn('admin')
  await page.getByRole('link', { name: 'Configuration' }).click()
  await page.getByRole('link', { name: 'Collections' }).click()
  const notes = page.getByTestId('col-main-notes')
  await notes.getByText('notes', { exact: true }).click()
  const writes: string[] = []
  page.on('request', (req) => {
    if (req.method() !== 'GET') writes.push(`${req.method()} ${req.url()}`)
  })

  await notes.getByRole('button', { name: 'Prepare a change to schema.json' }).click()
  const editor = notes.getByLabel('notes schema.json')
  const original = JSON.parse(await editor.inputValue())
  expect(original.properties.title.minLength).toBe(1)
  original.properties.title.minLength = 3
  await editor.fill(JSON.stringify(original, null, 2))
  await expect(notes.getByText('Changed from what the server runs.')).toBeVisible()
  const download = page.waitForEvent('download')
  await notes.getByRole('button', { name: 'Download schema.json' }).click()
  const file = await download
  expect(file.suggestedFilename()).toBe('schema.json')
  const saved = JSON.parse((await import('node:fs')).readFileSync((await file.path())!, 'utf8'))
  expect(saved.properties.title.minLength).toBe(3)

  // Invalid JSON can't be downloaded; starting again restores what the server runs.
  await editor.fill('{"type": ')
  await expect(notes.getByRole('alert')).toContainText('Not valid JSON')
  await expect(notes.getByRole('button', { name: 'Download schema.json' })).toBeDisabled()
  await notes.getByRole('button', { name: 'Start again' }).click()
  expect(JSON.parse(await editor.inputValue()).properties.title.minLength).toBe(1)

  // Rules are rebuilt from the expressions the server runs.
  await notes.getByRole('button', { name: 'Prepare a change to rules.yaml' }).click()
  const rules = notes.getByLabel('notes rules.yaml')
  const text = await rules.inputValue()
  expect(text).toContain('read: "user != nil"')
  expect(text).toContain('update: "user != nil && document._meta.owner == user.id"')
  expect(writes).toEqual([]) // the server was never written to
})
