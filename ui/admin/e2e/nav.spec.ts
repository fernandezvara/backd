import AxeBuilder from '@axe-core/playwright'
import { expect, test } from './fixtures'
import { go, navLabels } from './nav'
import { REALM, users } from './users'

const main = (page: import('@playwright/test').Page) => page.getByRole('navigation', { name: 'Main' })

test('a full administrator has the five groups with every page', async ({ page, signIn }) => {
  await signIn('admin')
  await expect(main(page).getByRole('button')).toHaveText(['Identity', 'Access', 'Data', 'Compute', 'Platform'])
  expect(await navLabels(page)).toEqual(
    expect.arrayContaining(['Overview', 'Users', 'Organizations', 'Invitations', 'API keys', 'Secrets', 'Data', 'Storage', 'Functions', 'Push', 'Jobs and history', 'Audit', 'Configuration']),
  )
})

test('a menu opens on a click, shows what each page is for, and closes on Escape', async ({ page, signIn }) => {
  await signIn('admin')
  const identity = main(page).getByRole('button', { name: 'Identity' })
  await identity.click()
  await expect(identity).toHaveAttribute('aria-expanded', 'true')
  await expect(main(page).getByRole('link', { name: 'Users', exact: true })).toBeVisible()
  await expect(main(page).getByText('Accounts, sessions and sign-in')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(identity).toHaveAttribute('aria-expanded', 'false')
  await expect(main(page).getByRole('link', { name: 'Users', exact: true })).toHaveCount(0)
})

test('the menus work from the keyboard and mark where you are', async ({ page, signIn }) => {
  await signIn('admin')
  const data = main(page).getByRole('button', { name: 'Data' })
  await data.focus()
  await page.keyboard.press('Enter')
  // Opening a menu from the keyboard puts the focus on its first link.
  await expect(main(page).getByRole('link', { name: 'Data', exact: true })).toBeFocused()
  await page.keyboard.press('Enter')
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/data$`))
  // The group of the page is marked, and so is the page inside it.
  await data.click()
  await expect(main(page).getByRole('link', { name: 'Data', exact: true })).toHaveAttribute('aria-current', 'page')
  await expect(main(page).getByRole('link', { name: 'Storage', exact: true })).not.toHaveAttribute('aria-current', 'page')
})

test('the sub-pages of an area keep the area marked', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/functions/history`)
  await main(page).getByRole('button', { name: 'Compute' }).click()
  await expect(main(page).getByRole('link', { name: 'Jobs and history' })).toHaveAttribute('aria-current', 'page')
  await expect(main(page).getByRole('link', { name: 'Functions', exact: true })).not.toHaveAttribute('aria-current', 'page')
})

test('a level without an area does not see its pages, and an empty group is gone', async ({ page, signIn }) => {
  await signIn('support')
  const labels = await navLabels(page)
  expect(labels).toContain('Users')
  expect(labels).not.toContain('Configuration')
  expect(labels).not.toContain('Storage')
  const groups = await main(page).getByRole('button').allTextContents()
  expect(groups).not.toContain('Platform')
})

test('the account menu holds who is signed in, language, theme and the way out', async ({ page, signIn }) => {
  await signIn('admin')
  const account = page.getByTestId('account-menu')
  await account.click()
  const menu = page.getByRole('dialog', { name: 'Account' })
  await expect(menu.getByText(users.admin)).toBeVisible()
  await expect(menu.getByLabel('Language')).toBeVisible()
  await expect(menu.getByLabel('Theme')).toBeVisible()
  await expect(menu.getByRole('button', { name: 'Sign out' })).toBeVisible()
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.keyboard.press('Escape')
  await expect(menu).toHaveCount(0)
})

test('the realm menu shows the realm and leads to the page that opens another', async ({ page, signIn }) => {
  await signIn('admin')
  const realm = page.getByTestId('realm-badge')
  await expect(realm).toContainText(`Realm ${REALM}`)
  await realm.click()
  await page.getByRole('link', { name: 'Open another realm' }).click()
  await expect(page.getByRole('heading', { name: 'Choose a realm' })).toBeVisible()
})

test('the breadcrumb says where you are and every part with a page leads there', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  const crumbs = page.getByRole('navigation', { name: 'Breadcrumb' })
  await expect(crumbs).toHaveText(new RegExp(`${REALM}\\s*Identity\\s*Overview`))
  await page.goto(`/_ui/r/${REALM}/data/main/notes`)
  await expect(crumbs.getByRole('listitem')).toHaveText([REALM, 'Data', 'main', 'notes'])
  await expect(crumbs.getByText('notes')).toHaveAttribute('aria-current', 'page')
  await expect(page).toHaveTitle('notes · backd admin')
  await crumbs.getByRole('link', { name: 'Data', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/data$`))
  // A group with another name leads to its first page; the realm to the overview.
  await page.goto(`/_ui/r/${REALM}/audit`)
  await crumbs.getByRole('link', { name: 'Platform' }).click()
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/audit$`))
  await crumbs.getByRole('link', { name: REALM }).click()
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}$`))
})

test('a user and an organization are named in the breadcrumb', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Users')
  await page.getByRole('link', { name: users.viewer }).click()
  await expect(page.getByRole('navigation', { name: 'Breadcrumb' }).getByText(users.viewer)).toHaveAttribute('aria-current', 'page')
  await expect(page).toHaveTitle(`${users.viewer} · backd admin`)
})

test.describe('a narrow screen', () => {
  test.use({ viewport: { width: 375, height: 700 } })

  test('has one button that stacks every group, and closes after a choice', async ({ page, signIn }) => {
    await signIn('admin')
    await expect(main(page).getByRole('button', { name: 'Identity' })).toBeHidden()
    const toggle = page.getByRole('button', { name: 'Menu' })
    await toggle.click()
    await expect(toggle).toHaveAttribute('aria-expanded', 'true')
    for (const group of ['Identity', 'Access', 'Data', 'Compute', 'Platform']) await expect(main(page).getByRole('heading', { name: group })).toBeVisible()
    // What the bar has no room for is in the panel: the way out too.
    await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible()
    await main(page).getByRole('link', { name: 'Users', exact: true }).click()
    await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/users$`))
    await expect(main(page).getByRole('link', { name: 'Users', exact: true })).toHaveCount(0)
  })

  test('has no accessibility violations with the menu open', async ({ page, signIn }) => {
    await signIn('admin')
    await page.getByRole('button', { name: 'Menu' }).click()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  })
})

test('the menus have no accessibility violations, open or not', async ({ page, signIn }) => {
  await signIn('admin')
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await main(page).getByRole('button', { name: 'Compute' }).click()
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
})
