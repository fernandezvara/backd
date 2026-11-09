import AxeBuilder from '@axe-core/playwright'
import { expect, test } from './fixtures'
import { REALM } from './users'

// A dev-mode backd whose config hasn't loaded answers every call with 503 config_not_loaded
// (the example stack has loaded, so the answer is played here). The interface says so and
// links the report instead of a sign-in that can't work, and carries on once it loads.
test('a server whose config has not loaded is shown as such, not as a sign-in', async ({ page }) => {
  const message = 'the config has not loaded yet (dev mode): fix what the report says and save'
  let loaded = false
  await page.route('**/readyz', async (route) => {
    if (loaded) return route.fallback()
    await route.fulfill({
      status: 503,
      contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'config_not_loaded', message, details: [{ path: 'app/realm.yaml:2', reason: 'invalid YAML', code: 'config' }], request_id: 'e2e' } }),
    })
  })
  await page.goto(`/_ui/r/${REALM}/signin`)
  await expect(page.getByTestId('config-not-loaded')).toContainText(message)
  await expect(page.getByRole('link', { name: 'Open the report' })).toHaveAttribute('href', '/')
  await expect(page.getByRole('button', { name: 'Sign in' })).toHaveCount(0)
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])

  loaded = true
  await expect(page.getByRole('button', { name: 'Sign in' })).toBeVisible({ timeout: 10_000 })
  await expect(page.getByTestId('config-not-loaded')).toHaveCount(0)
})
