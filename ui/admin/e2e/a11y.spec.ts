import AxeBuilder from '@axe-core/playwright'
import { expect, test } from './fixtures'
import { REALM } from './users'
import { setTheme } from './nav'

// Every page of the sidebar and its sub-pages, in both themes, must pass the accessibility checks
// (contrast included: the sidebar paints with the accent, and the dark theme has its own greys).
const PAGES = [
  '', 'users', 'orgs', 'invitations', 'apikeys', 'secrets', 'security', 'audit', 'compliance', 'app-config',
  'config', 'config/settings', 'config/signin', 'config/templates', 'config/files', 'config/backup', 'config/api',
  'data', 'data/checks', 'data/main/products', 'data/main/labels', 'data/main/products/new', 'storage',
  'functions', 'functions/jobs', 'functions/history', 'functions/triggers', 'push', 'account',
]

for (const theme of ['Light', 'Dark']) {
  test(`${theme}: every page has no accessibility violations`, async ({ page, signIn }) => {
    test.setTimeout(300_000)
    await signIn('admin', { keep: true })
    await setTheme(page, theme)
    const failures: string[] = []
    for (const p of PAGES) {
      await page.goto(`/_ui/r/${REALM}/${p}`)
      await page.getByRole('navigation', { name: 'Main' }).waitFor()
      await page.waitForLoadState('networkidle')
      for (const v of (await new AxeBuilder({ page }).analyze()).violations) failures.push(`/${p}: ${v.id} ${v.nodes.map((n) => n.target.join(' ')).slice(0, 3).join(' | ')}`)
    }
    expect(failures).toEqual([])
  })
}

for (const accent of ['blue', 'rose', 'orange']) {
  test(`the ${accent} accent keeps the sidebar and the buttons readable`, async ({ page, signIn }) => {
    await signIn('admin', { keep: true })
    await page.evaluate((a) => localStorage.setItem('backd-admin:accent', a), accent)
    await page.reload()
    await expect(page.locator('html')).toHaveAttribute('data-accent', accent)
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.goto(`/_ui/r/${REALM}/data/main/products`)
    await page.getByTestId('filter-toggle').click()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.evaluate(() => localStorage.removeItem('backd-admin:accent'))
  })
}
