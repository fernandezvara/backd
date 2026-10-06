import { expect, test } from './fixtures'
import { REALM } from './users'

// scripts/ui-e2e.sh puts one document in `labels` that no longer matches its
// schema (its name is a number): the check must find it, and only it.

test('a collection is checked against its schema, with a warning first, and the report names the document', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/labels`)
  const panel = page.getByTestId('schema-check')
  await expect(panel).toContainText('Never checked.')

  // The warning comes before anything starts.
  await panel.getByRole('button', { name: 'Check documents against the schema' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByTestId('check-warning')).toContainText('on a big collection it can take a long time')
  await expect(dialog.getByTestId('check-scope')).toHaveText('main/labels')
  await dialog.getByRole('button', { name: 'Start the check' }).click()

  // It runs in the background; the report names the document, the path and the rule, never the value.
  const invalid = panel.getByTestId('invalid-drifted1')
  await expect(invalid).toBeVisible({ timeout: 30_000 })
  await expect(invalid).toContainText('name')
  await expect(invalid).toContainText('got number, want string')
  await expect(invalid).not.toContainText('42')
  await expect(panel.getByTestId('check-report')).toContainText('1 invalid')

  // The id opens the document.
  await invalid.getByRole('link', { name: 'drifted1' }).click()
  await expect(page).toHaveURL(/data\/main\/labels\/drifted1/)

  // The page of every collection keeps the latest report.
  await page.goto(`/_ui/r/${REALM}/data/checks`)
  const row = page.getByTestId('check-main-labels')
  await expect(row).toContainText('1')
  await page.getByTestId('report-main-labels').locator('summary').click()
  await expect(page.getByTestId('invalid-drifted1')).toBeVisible()
  // A collection that was never checked says so.
  await expect(page.getByTestId('check-main-notes')).toContainText('Never checked.')
})

test('a read-only administrator reads the reports and may start a check', async ({ page, signIn }) => {
  await signIn('viewer', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/checks`)
  await expect(page.getByTestId('check-main-labels')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Check every collection' })).toBeEnabled()
  await page.goto(`/_ui/r/${REALM}/data/main/labels`)
  await expect(page.getByTestId('schema-check')).toBeVisible()
})

test('a level without the data area has no way to the schema checks', async ({ page, signIn }) => {
  await signIn('support', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/checks`)
  await expect(page.getByRole('heading', { name: 'Schema checks' })).toHaveCount(0)
})

test('the schema check views have no accessibility violations', async ({ page, signIn }) => {
  const { default: AxeBuilder } = await import('@axe-core/playwright')
  await signIn('admin', { keep: true })
  for (const theme of ['Light', 'Dark']) {
    await page.getByLabel('Theme').selectOption({ label: theme })
    await page.goto(`/_ui/r/${REALM}/data/checks`)
    await expect(page.getByTestId('checks-table')).toBeVisible()
    await page.getByTestId('report-main-labels').locator('summary').click()
    await expect(page.getByTestId('invalid-drifted1')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.getByTestId('check-all').click()
    await expect(page.getByRole('dialog')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.keyboard.press('Escape')
    await page.goto(`/_ui/r/${REALM}/data/main/labels`)
    await expect(page.getByTestId('schema-check')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  }
})
