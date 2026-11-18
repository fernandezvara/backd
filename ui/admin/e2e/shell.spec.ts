import AxeBuilder from '@axe-core/playwright'
import { expect, test } from './fixtures'
import { setTheme } from './nav'
import { REALM } from './users'

// The shell (sidebar, breadcrumb, tabs) with the page of each area, in both themes, and on a narrow
// screen with the drawer open.
const PAGES = ['users', 'apikeys', 'data', 'functions', 'audit', 'config'] as const
const clean = async (page: import('@playwright/test').Page) => expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])

for (const theme of ['Light', 'Dark']) {
  test(`${theme}: every area has no accessibility violations`, async ({ page, signIn }) => {
    await signIn('admin', { keep: true })
    await setTheme(page, theme)
    for (const area of PAGES) {
      await page.goto(`/_ui/r/${REALM}/${area}`)
      await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toBeVisible()
      await clean(page)
    }
  })
}

test.describe('a narrow screen', () => {
  test.use({ viewport: { width: 375, height: 700 } })

  test('every area, with the menu closed and open, has no violations and no sideways scroll', async ({ page, signIn }) => {
    await signIn('admin', { keep: true })
    for (const area of PAGES) {
      await page.goto(`/_ui/r/${REALM}/${area}`)
      await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toBeVisible()
      await clean(page)
      // The page may scroll inside its tables, never as a whole.
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth), `${area} fits the width`).toBe(true)
      await page.getByTestId('nav-toggle').click()
      await clean(page)
      await page.getByRole('button', { name: 'Close the menu' }).click()
    }
  })
})

test('the keyboard reaches the sidebar, the breadcrumb and the tabs, in that order', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/functions/jobs`)
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' })).toBeVisible()
  await page.locator('body').click({ position: { x: 1, y: 1 } })
  const seen: string[] = []
  for (let i = 0; i < 60; i++) {
    await page.keyboard.press('Tab')
    seen.push(await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null
      const inside = (sel: string) => (el?.closest(sel) ? sel : '')
      return `${inside('nav[aria-label="Main"]') || inside('nav[aria-label="Breadcrumb"]') || inside('nav[aria-label="Functions sections"]')}|${el?.textContent?.trim().slice(0, 20) ?? ''}`
    }))
  }
  const at = (needle: string) => seen.findIndex((s) => s.includes(needle))
  expect(at('nav[aria-label="Main"]|Overview'), 'the first link of the sidebar is reached').toBeGreaterThanOrEqual(0)
  expect(at('nav[aria-label="Main"]|Configuration'), 'the last link of the sidebar is reached').toBeGreaterThan(at('Overview'))
  expect(at('nav[aria-label="Breadcrumb"]'), 'the breadcrumb is reached after the sidebar').toBeGreaterThan(at('Configuration'))
  expect(at('Functions sections') >= 0, 'the tab strip is reached').toBe(true)
})
