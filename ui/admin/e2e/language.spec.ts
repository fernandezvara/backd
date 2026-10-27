import AxeBuilder from '@axe-core/playwright'
import { expect, test } from './fixtures'
import { PASSWORD, REALM, users } from './users'
import { go, inAccount, navLabels, setLanguage, setTheme } from './nav'

test.describe('a Spanish browser', () => {
  test.use({ locale: 'es-ES' })

  test('gets the interface in Spanish with no choice made', async ({ page }) => {
    await page.goto(`/_ui/r/${REALM}/signin`)
    await expect(page.getByRole('heading', { name: `Inicia sesión en ${REALM}` })).toBeVisible()
    await expect(page.locator('html')).toHaveAttribute('lang', 'es')
    await page.getByLabel('Correo electrónico').fill(users.admin)
    await page.getByLabel('Contraseña').fill(PASSWORD)
    await page.getByRole('button', { name: 'Iniciar sesión' }).click()
    await expect(page.getByRole('button', { name: 'Cuenta' })).toBeVisible()
    expect(await navLabels(page)).toContain('Usuarios')
    await expect(page.getByTestId('level')).toHaveText('Administrador completo')
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  })
})

test('the language can be chosen, and the choice is kept', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  expect(await navLabels(page)).toContain('Users')
  await expect(page.locator('html')).toHaveAttribute('lang', 'en')

  await setLanguage(page, 'es')
  expect(await navLabels(page)).toContain('Usuarios')
  await expect(page.getByRole('button', { name: 'Cuenta' })).toBeVisible()
  await expect(page.locator('html')).toHaveAttribute('lang', 'es')
  // The choices are written in their own language.
  await inAccount(page, async () => {
    await expect(page.getByTestId('language-switch').locator('option')).toHaveText(['Automático', 'English', 'Español'])
  })

  // Pages, and the dates on them, follow.
  await go(page, 'Usuarios')
  await expect(page.getByRole('heading', { name: 'Usuarios' })).toBeVisible()
  await expect(page.getByTestId('users-table')).toContainText(users.admin)
  expect(await page.getByTestId('users-table').innerText()).toMatch(/\b(ene|feb|mar|abr|may|jun|jul|ago|sept?|oct|nov|dic)\b/)

  // It survives a reload (and keeps the session, which this tab opted in to).
  await page.reload()
  await expect(page.getByRole('heading', { name: 'Usuarios' })).toBeVisible()
  await inAccount(page, async () => {
    await expect(page.getByTestId('language-switch')).toHaveValue('es')
  })

  // Back to following the browser, English here.
  await setLanguage(page, 'auto')
  await expect(page.getByRole('heading', { name: 'Users' })).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('backd-admin:language'))).toBeNull()
})

test('typed confirmations are typed in the language shown', async ({ page, signIn }) => {
  await signIn('admin')
  await setLanguage(page, 'es')
  await go(page, 'Invitaciones')
  await page.getByRole('button', { name: 'Crear invitación' }).click()
  await page.getByRole('dialog').getByRole('button', { name: 'Crear', exact: true }).click()
  await expect(page.getByTestId('invitation-token')).toHaveValue(/^bdi_/)
  await page.getByRole('dialog').getByRole('button', { name: 'Hecho' }).click()
  await page.getByTestId('invitations-table').getByRole('button', { name: 'Revocar' }).first().click()
  await page.getByRole('dialog').getByLabel('Escribe revocar para confirmar').fill('revocar')
  await page.getByRole('dialog').getByRole('button', { name: 'Revocar invitación' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Invitación revocada.' })).toBeVisible()
})

test('the Spanish views have no accessibility violations', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await setLanguage(page, 'es')
  for (const theme of ['Claro', 'Oscuro']) {
    await setTheme(page, theme)
    for (const [url, ready] of [
      ['users', 'users-table'],
      ['apikeys', 'apikeys-table'],
      ['audit', 'audit-table'],
      ['config/collections', 'col-main-products'],
      ['data/main/products', 'docs-table'],
    ] as const) {
      await page.goto(`/_ui/r/${REALM}/${url}`)
      await expect(page.getByTestId(ready).first()).toBeVisible()
      expect((await new AxeBuilder({ page }).analyze()).violations, url).toEqual([])
    }
  }
})
