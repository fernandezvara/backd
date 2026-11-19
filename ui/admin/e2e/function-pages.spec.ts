import { expect, test } from './fixtures'
import { REALM } from './users'
import { go } from './nav'

// A function's own page (tabs with the code, the runs, the settings and who uses it), and the
// pages of triggers and of history that sit with the jobs.

test('a function has its tabs: code read-only, invocations, settings and who uses it', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Functions')
  await page.getByTestId('fn-main-greet').getByRole('link', { name: 'greet' }).first().click()
  await expect(page.getByTestId('function-name')).toContainText('greet')
  await expect(page.getByRole('tab')).toHaveText(['Code', 'Invocations', 'Settings', 'Used by'])

  // The code is read-only: files of the function and the source with line numbers.
  await expect(page.getByTestId('fn-files')).toContainText('index.ts')
  await expect(page.getByTestId('fn-files')).toContainText('function.yaml')
  await expect(page.getByTestId('fn-source')).toContainText('export default')
  await expect(page.getByText('read-only').first()).toBeVisible()
  await expect(page.getByRole('button', { name: /^(Save|Edit)/ })).toHaveCount(0)

  for (const tab of ['invocations', 'settings', 'usedby'] as const) {
    await page.getByTestId(`tab-${tab}`).click()
    await expect(page.getByTestId(`tab-${tab}`)).toHaveAttribute('aria-selected', 'true')
  }
  await page.getByTestId('back-to-functions').click()
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/functions$`))
})

test('the jobs area has definitions, triggers, history and the jobs themselves', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await go(page, 'Jobs and history')
  await expect(page.getByTestId('jobs-table')).toBeVisible()
  const sections = page.getByRole('navigation', { name: 'Functions sections' })
  await expect(sections.getByRole('link')).toHaveText(['Definitions', 'Triggers', 'History', 'Jobs'])

  await sections.getByRole('link', { name: 'Triggers', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/functions/triggers$`))
  await expect(page.getByRole('heading', { name: 'Triggers' }).first()).toBeVisible()
  // The trigger the adminui realm declares on labels is listed with its function.
  await expect(page.getByText('main/labels').first()).toBeVisible()

  await sections.getByRole('link', { name: 'History', exact: true }).click()
  await expect(page.getByTestId('history-table')).toBeVisible()
})
