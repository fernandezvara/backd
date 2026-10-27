import AxeBuilder from '@axe-core/playwright'
import { expect, test } from './fixtures'
import { navLabels } from './nav'
import { REALM } from './users'

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
