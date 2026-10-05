import { mkdirSync } from 'node:fs'
import { expect, test } from './fixtures'
import { REALM } from './users'

// The pictures of the documentation's Admin UI guide, in the light theme.
// Not part of the usual run: `E2E_SCREENSHOTS=1 make ui-e2e` regenerates
// docs/static/admin-ui/*.png, which are committed.
test.skip(!process.env.E2E_SCREENSHOTS, 'only when E2E_SCREENSHOTS is set')
test.use({ viewport: { width: 1200, height: 720 }, colorScheme: 'light' })

const out = (name: string) => {
  mkdirSync('../../docs/static/admin-ui', { recursive: true })
  return `../../docs/static/admin-ui/${name}.png`
}

test('screenshots of the main views', async ({ page, signIn }) => {
  await page.goto(`/_ui/r/${REALM}/signin`)
  await page.getByLabel('Theme').selectOption({ label: 'Light' })
  await page.screenshot({ path: out('signin') })

  await signIn('admin', { keep: true })
  await page.getByLabel('Theme').selectOption({ label: 'Light' })
  await expect(page.getByTestId('level')).toBeVisible()
  await page.screenshot({ path: out('overview') })

  const shoot = async (url: string, ready: string, name: string) => {
    await page.goto(`/_ui/r/${REALM}/${url}`)
    await expect(page.getByTestId(ready).first()).toBeVisible()
    await page.screenshot({ path: out(name) })
  }
  await shoot('users', 'users-table', 'users')
  await shoot('apikeys', 'apikeys-table', 'apikeys')
  await shoot('audit', 'audit-table', 'audit')
  await shoot('functions', 'fn-main-greet', 'functions')
  await shoot('functions/history', 'history-table', 'history')
  await shoot('config/collections', 'col-main-products', 'config')
  await page.goto(`/_ui/r/${REALM}/data/main/products`)
  await expect(page.getByTestId('docs-table')).toBeVisible()

  // The query builder with a condition, and a document as a form.
  await page.getByRole('button', { name: 'Add condition' }).click()
  const row = page.getByTestId('condition').first()
  await row.getByLabel('Field').selectOption('name')
  await row.getByLabel('Operator').selectOption('$icontains')
  await row.getByLabel('Value').fill('widget')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByTestId('total')).toHaveText('1 documents')
  await page.screenshot({ path: out('query') })
  await page.getByRole('link', { name: /^[0-9a-v]{20}$/ }).first().click()
  await expect(page.getByTestId('doc-version')).toBeVisible()
  await page.setViewportSize({ width: 1200, height: 1150 })
  await page.screenshot({ path: out('document') })
  await page.setViewportSize({ width: 1200, height: 720 })

  // A user's page.
  await page.goto(`/_ui/r/${REALM}/users`)
  await page.getByRole('link', { name: 'viewer@adminui.example' }).click()
  await expect(page.getByTestId('user-email')).toBeVisible()
  await page.screenshot({ path: out('user') })
})
