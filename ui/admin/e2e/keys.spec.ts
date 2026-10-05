import { expect, test } from './fixtures'
import { REALM } from './users'

const stamp = Date.now().toString(36)

test('API keys: create shows the key once, revoke asks for the name', async ({ page, signIn }) => {
  const name = `e2e-${stamp}`
  await signIn('admin')
  await page.getByRole('link', { name: 'API keys' }).click()
  await page.getByRole('button', { name: 'Create API key' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill(name)
  await dialog.getByLabel('Expires in').fill('90d')
  await dialog.getByLabel('Allowed networks').fill('10.0.0.0/8, 192.168.0.0/16')
  await dialog.getByRole('button', { name: 'Create' }).click()

  const shown = page.getByTestId('api-key')
  await expect(shown).toHaveValue(/^bdk_/)
  const key = await shown.inputValue()
  await page.getByRole('button', { name: 'I have copied it' }).click()

  const row = page.getByTestId('apikeys-table').getByRole('row', { name })
  await expect(row).toContainText('data')
  await expect(row).toContainText('10.0.0.0/8, 192.168.0.0/16')
  await expect(row).toContainText(key.slice(0, 8))
  // The key itself is not on the page any more.
  await expect(page.locator('body')).not.toContainText(key)

  await row.getByRole('button', { name: `Revoke ${name}` }).click()
  const confirm = page.getByRole('dialog')
  await confirm.getByLabel(`Type ${name} to confirm`).fill('wrong')
  await confirm.getByRole('button', { name: 'Revoke key' }).click()
  await expect(confirm.getByText("The text doesn't match.")).toBeVisible()
  await confirm.getByLabel(`Type ${name} to confirm`).fill(name)
  await confirm.getByRole('button', { name: 'Revoke key' }).click()
  await expect(page.getByTestId('apikeys-table').getByRole('row', { name })).toHaveCount(0)
})

test('secrets: set is write-only, delete asks for the name', async ({ page, signIn }) => {
  const name = `E2E_${stamp.toUpperCase()}`
  const value = `s3cret-${stamp}`
  await signIn('admin')
  await page.getByRole('link', { name: 'Secrets' }).click()
  await page.getByRole('button', { name: 'Set a secret' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill(name)
  await dialog.getByLabel('Value').fill(value)
  await expect(dialog.getByLabel('Value')).toHaveAttribute('type', 'password')
  await dialog.getByRole('button', { name: 'Save' }).click()

  const row = page.getByTestId('secrets-table').getByRole('row', { name })
  await expect(row).toContainText('Realm (every database)')
  await expect(row).toContainText('user:')
  await expect(page.locator('body')).not.toContainText(value)

  await row.getByRole('button', { name: `Delete ${name}` }).click()
  const confirm = page.getByRole('dialog')
  await confirm.getByLabel(`Type ${name} to confirm`).fill(name)
  await confirm.getByRole('button', { name: 'Delete secret' }).click()
  await expect(page.getByTestId('secrets-table').getByRole('row', { name })).toHaveCount(0)
})

test('audit: filters, details as text and paging', async ({ page, signIn }) => {
  await signIn('admin')
  await page.getByRole('link', { name: 'Audit' }).click()
  await expect(page.getByTestId('audit-table')).toContainText('admin.login')

  await page.getByLabel('Action').fill('admin.login')
  await page.getByRole('button', { name: 'Filter' }).click()
  const rows = page.getByTestId('audit-table').locator('tbody tr')
  await expect(rows.first()).toContainText('admin.login')
  for (const text of await rows.locator('td:nth-child(2)').allTextContents()) expect(text).toBe('admin.login')

  await page.getByLabel('Action').fill('no.such.action')
  await page.getByRole('button', { name: 'Filter' }).click()
  await expect(page.getByText('No records match.')).toBeVisible()

  await page.getByRole('button', { name: 'Clear' }).click()
  await expect(page.getByTestId('audit-table')).toContainText('admin.login')
  await expect(page.getByRole('button', { name: 'Previous' })).toBeDisabled()
})

test('audit details are text, never markup', async ({ page, signIn, request }) => {
  // A key name can't hold markup, but whatever an actor writes must stay inert:
  // the target of a failed login is the email typed, so type some HTML there.
  const payload = '<img src=x onerror=window.__pwned=1>@adminui.example'
  await request.post(`/v1/${REALM}/_auth/login`, { data: { email: payload, password: 'nope' } })
  await signIn('admin')
  await page.getByRole('link', { name: 'Audit' }).click()
  await expect(page.getByTestId('audit-table')).toBeVisible()
  expect(await page.evaluate(() => (window as unknown as { __pwned?: number }).__pwned)).toBeUndefined()
  await expect(page.locator('img[src="x"]')).toHaveCount(0)
})

test('a read-only administrator reads keys, secrets and audit with no controls', async ({ page, signIn }) => {
  await signIn('viewer')
  for (const [link, table] of [['API keys', 'apikeys-table'], ['Secrets', 'secrets-table'], ['Audit', 'audit-table']] as const) {
    await page.getByRole('link', { name: link, exact: true }).click()
    await expect(page.getByTestId(table)).toBeVisible()
  }
  await page.getByRole('link', { name: 'API keys' }).click()
  await expect(page.getByRole('button', { name: /^(Create API key|Revoke)/ })).toHaveCount(0)
  await page.getByRole('link', { name: 'Secrets' }).click()
  await expect(page.getByRole('button', { name: /^(Set a secret|Delete)/ })).toHaveCount(0)
})

test('a custom level without these areas has no way in', async ({ page, signIn }) => {
  await signIn('support')
  for (const link of ['API keys', 'Secrets', 'Audit']) await expect(page.getByRole('link', { name: link, exact: true })).toHaveCount(0)
  await page.goto(`/_ui/r/${REALM}/audit`)
  await page.waitForURL(new RegExp(`/_ui/r/${REALM}/signin`)) // a fresh load has no session in memory
})

test('the keys, secrets and audit views have no accessibility violations', async ({ page, signIn }) => {
  const { default: AxeBuilder } = await import('@axe-core/playwright')
  await signIn('admin')
  for (const theme of ['Light', 'Dark']) {
    await page.getByLabel('Theme').selectOption({ label: theme })
    for (const [link, table] of [['API keys', 'apikeys-table'], ['Secrets', 'secrets-table'], ['Audit', 'audit-table']] as const) {
      await page.getByRole('link', { name: link, exact: true }).click()
      await expect(page.getByTestId(table)).toBeVisible()
      expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    }
    await page.getByRole('link', { name: 'API keys' }).click()
    await page.getByRole('button', { name: 'Create API key' }).click()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.keyboard.press('Escape')
  }
})
