import { expect, test } from './fixtures'
import { REALM, users } from './users'
import { openAccount } from './nav'

// The appearance is kept in this browser (localStorage), chosen on the Account page.

test('the Account page offers the theme, the language and the accent, and applies them at once', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await openAccount(page)
  const html = page.locator('html')

  await page.getByTestId('theme-dark').click()
  await expect(html).toHaveClass(/dark/)
  await page.getByTestId('theme-light').click()
  await expect(html).not.toHaveClass(/dark/)

  await page.getByTestId('accent-rose').click()
  await expect(html).toHaveAttribute('data-accent', 'rose')
  await expect(page.getByTestId('accent-rose')).toHaveAttribute('aria-pressed', 'true')
  await expect(page.getByTestId('accent-teal')).toHaveAttribute('aria-pressed', 'false')

  // The preview shows the choice without leaving the page.
  const preview = page.getByTestId('preferences').getByText('Primary action')
  await expect(preview).toBeVisible()
})

test('the choices are kept: they survive a reload and are the browser\'s, not the account\'s', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await openAccount(page)
  await page.getByTestId('accent-violet').click()
  await page.getByTestId('theme-dark').click()
  await page.getByTestId('language-es').click()
  await expect(page.getByTestId('language-es')).toHaveAttribute('aria-checked', 'true')
  expect(await page.evaluate(() => [localStorage.getItem('backd-admin:accent'), localStorage.getItem('backd-admin:theme'), localStorage.getItem('backd-admin:language')])).toEqual(['violet', 'dark', 'es'])

  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('data-accent', 'violet')
  await expect(page.locator('html')).toHaveClass(/dark/)
  await expect(page.locator('html')).toHaveAttribute('lang', 'es')
  await expect(page.getByRole('heading', { name: 'Cuenta', exact: true })).toBeVisible()

  // Back to the defaults for the tests that follow.
  await page.getByTestId('language-auto').click()
  await page.getByTestId('theme-system').click()
  await page.getByTestId('accent-teal').click()
})

test('the top bar has a quick theme button that goes round system, light and dark', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  const toggle = page.getByTestId('theme-toggle')
  await page.evaluate(() => localStorage.setItem('backd-admin:theme', 'system'))
  await page.reload()
  await expect(toggle).toHaveAttribute('aria-label', /System/)
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-label', /Light/)
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-label', /Dark/)
  await expect(page.locator('html')).toHaveClass(/dark/)
  await toggle.click()
  await expect(toggle).toHaveAttribute('aria-label', /System/)
})

test('the sign-in page has the theme and the accent too, before anyone is signed in', async ({ page }) => {
  await page.goto(`/_ui/r/${REALM}/signin`)
  await page.getByTestId('accent-orange').click()
  await expect(page.locator('html')).toHaveAttribute('data-accent', 'orange')
  await page.getByRole('radio', { name: 'Dark', exact: true }).click()
  await expect(page.locator('html')).toHaveClass(/dark/)
  await page.evaluate(() => localStorage.clear())
})

test('the page says who is signed in and lists the passkeys of the account', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await openAccount(page)
  await expect(page.getByRole('heading', { name: 'Account', exact: true })).toBeVisible()
  await expect(page.getByTestId('passkey-add')).toBeVisible()
  await expect(page.getByText(users.admin).first()).toBeVisible()
})
