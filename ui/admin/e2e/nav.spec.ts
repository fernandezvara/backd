import AxeBuilder from '@axe-core/playwright'
import { expect, test } from './fixtures'
import { go, navLabels } from './nav'
import { REALM, users } from './users'

const main = (page: import('@playwright/test').Page) => page.getByRole('navigation', { name: 'Main' })

test('a full administrator has the groups of the sidebar with every page', async ({ page, signIn }) => {
  await signIn('admin')
  // The overview heads the sidebar with no heading of its own; the other groups have one.
  await expect(main(page).getByRole('heading')).toHaveText(['Data', 'Compute', 'Identity', 'Access', 'Platform'])
  expect(await navLabels(page)).toEqual([
    'Overview', 'Data', 'Storage', 'Functions', 'Jobs and history', 'Push', 'Users', 'Organizations', 'Invitations', 'API keys', 'Secrets', 'Security', 'Audit', 'Compliance', 'App config', 'Configuration',
  ])
})

test('the sidebar marks where you are, and the sub-pages of an area keep the area marked', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await go(page, 'Data')
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/data$`))
  await expect(main(page).getByRole('link', { name: 'Data', exact: true })).toHaveAttribute('aria-current', 'page')
  await expect(main(page).getByRole('link', { name: 'Storage', exact: true })).not.toHaveAttribute('aria-current', 'page')
  await page.goto(`/_ui/r/${REALM}/functions/history`)
  await expect(main(page).getByRole('link', { name: 'Jobs and history' })).toHaveAttribute('aria-current', 'page')
  await expect(main(page).getByRole('link', { name: 'Functions', exact: true })).not.toHaveAttribute('aria-current', 'page')
})

test('a level without an area does not see its pages, and an empty group is gone', async ({ page, signIn }) => {
  await signIn('support')
  const labels = await navLabels(page)
  expect(labels).toContain('Users')
  expect(labels).not.toContain('Configuration')
  expect(labels).not.toContain('Storage')
  expect(labels).not.toContain('Audit')
  // Users also open the security and compliance pages; what they cannot read leaves its group out.
  const groups = await main(page).getByRole('heading').allTextContents()
  expect(groups).toEqual(['Identity', 'Access', 'Platform'])
  await expect(main(page).getByRole('heading', { name: 'Data' })).toHaveCount(0)
  await expect(main(page).getByRole('heading', { name: 'Compute' })).toHaveCount(0)
})

test('the sidebar collapses to its icons, keeps every link named, and remembers the choice', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await page.getByTestId('nav-collapse').click()
  await expect(page.getByTestId('nav-collapse')).toHaveAttribute('aria-pressed', 'true')
  // Collapsed, the links are icons: the name stays for screen readers and as the tooltip.
  await expect(main(page).getByRole('link', { name: 'Users', exact: true })).toBeVisible()
  await expect(main(page).getByRole('heading')).toHaveCount(0)
  await page.reload()
  await expect(page.getByTestId('nav-collapse')).toHaveAttribute('aria-pressed', 'true')
  await page.getByTestId('nav-collapse').click()
  await expect(page.getByTestId('nav-collapse')).toHaveAttribute('aria-pressed', 'false')
  await expect(main(page).getByRole('heading')).toHaveCount(5)
})

test('the account menu holds who is signed in, the Account page and the way out', async ({ page, signIn }) => {
  await signIn('admin')
  await page.getByTestId('account-menu').click()
  const menu = page.getByRole('dialog', { name: 'Account' })
  await expect(menu.getByText(users.admin)).toBeVisible()
  await expect(menu.getByTestId('account-link')).toBeVisible()
  await expect(menu.getByRole('button', { name: 'Sign out' })).toBeVisible()
  // The appearance is chosen on the Account page, not here; the top bar only has the quick theme button.
  await expect(menu.getByLabel('Language')).toHaveCount(0)
  await expect(page.getByTestId('theme-toggle')).toBeVisible()
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
})

test('the realm is the first part of the breadcrumb, and the switcher heads the sidebar', async ({ page, signIn }) => {
  await signIn('admin')
  await expect(page.getByTestId('realm-crumb')).toHaveText(`Realm: ${REALM}`)
  // The realm switcher heads the sidebar.
  await expect(page.getByTestId('realm-badge')).toBeVisible()
})

test('the breadcrumb says where you are and every part with a page leads there', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  const crumbs = page.getByRole('navigation', { name: 'Breadcrumb' })
  await expect(crumbs).toHaveText(new RegExp(`Realm: ${REALM}\\s*Overview`))
  await page.goto(`/_ui/r/${REALM}/data/main/notes`)
  await expect(crumbs.getByRole('listitem')).toHaveText([`Realm: ${REALM}`, 'Data', 'main', 'notes'])
  await expect(crumbs.getByText('notes')).toHaveAttribute('aria-current', 'page')
  await expect(page).toHaveTitle('notes · back(d) admin')
  await crumbs.getByRole('link', { name: 'Data', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/data$`))
  // A group with another name leads to its first page; the realm to the overview.
  await page.goto(`/_ui/r/${REALM}/audit`)
  await crumbs.getByRole('link', { name: 'Platform' }).click()
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/audit$`))
  await crumbs.getByRole('link', { name: `Realm: ${REALM}` }).click()
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}$`))
})

test('a user and an organization are named in the breadcrumb', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Users')
  await page.getByRole('link', { name: users.viewer }).click()
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByText(users.viewer)).toHaveAttribute('aria-current', 'page')
  await expect(page).toHaveTitle(`${users.viewer} · back(d) admin`)
})

test.describe('a narrow screen', () => {
  test.use({ viewport: { width: 375, height: 700 } })

  test('has one button that opens the sidebar as a drawer, and closes after a choice', async ({ page, signIn }) => {
    await signIn('admin')
    const toggle = page.getByTestId('nav-toggle')
    await expect(toggle).toBeVisible()
    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-expanded', 'true')
    for (const group of ['Data', 'Compute', 'Identity', 'Access', 'Platform']) await expect(main(page).getByRole('heading', { name: group })).toBeVisible()
    await main(page).getByRole('link', { name: 'Users', exact: true }).click()
    await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/users$`))
    await expect(toggle).toHaveAttribute('aria-expanded', 'false')
  })

  test('has no accessibility violations with the drawer open', async ({ page, signIn }) => {
    await signIn('admin')
    await page.getByTestId('nav-toggle').click()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  })
})

test('the command palette opens from the top bar, finds a page and goes there', async ({ page, signIn }) => {
  await signIn('admin')
  await page.getByTestId('palette-open').click()
  const palette = page.getByTestId('palette')
  await expect(palette).toBeVisible()
  await page.getByPlaceholder('Type a page, collection or function').fill('Audit')
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/audit$`))
  await expect(palette).toHaveCount(0)
})

test('the sidebar has no accessibility violations', async ({ page, signIn }) => {
  await signIn('admin')
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
})
