import { createClient } from 'backd-js'
import { expect, test } from './fixtures'
import { PASSWORD, REALM, users } from './users'
import { go } from './nav'

// A collection's page: Records, Schema, Rules, Triggers, Checks and API are tabs of one page, and a
// document's page has Form, JSON, Files and History. Everything on them is read from the server's
// configuration; nothing here changes it.

const base = process.env.E2E_URL ?? 'http://localhost:8080'

test('the data list shows the collections as cards with what each one has', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Data')
  const products = page.getByTestId('open-main-products')
  await expect(products).toContainText('products')
  await expect(products).toContainText('trash')
  await expect(page.getByTestId('open-main-notes')).toBeVisible()
  // The filter narrows the cards.
  await page.getByPlaceholder('Filter collections').fill('lab')
  await expect(page.getByTestId('open-main-labels')).toBeVisible()
  await expect(page.getByTestId('open-main-products')).toHaveCount(0)
  await page.getByPlaceholder('Filter collections').fill('zzz')
  await expect(page.getByText('No collection matches')).toBeVisible()
})

test('a collection has its tabs, and the schema, rules, triggers, checks and API are read from the configuration', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Data')
  await page.getByTestId('open-main-labels').click()
  await expect(page.getByTestId('collection-name')).toHaveText('labels')
  await expect(page.getByRole('tab')).toHaveText(['Records', 'Schema', 'Rules', 'Triggers', 'Checks', 'API'])

  await page.getByTestId('tab-schema').click()
  await expect(page.getByTestId('schema-fields')).toContainText('name')
  await expect(page.getByTestId('schema-fields')).toContainText('string')

  await page.getByTestId('tab-rules').click()
  await expect(page.locator('main')).toContainText('user != nil')
  await expect(page.locator('main')).toContainText('adminui/main/labels/collection.yaml')

  // The trigger the realm declares on this collection, with its event and function.
  await page.getByTestId('tab-triggers').click()
  const triggers = page.getByTestId('collection-triggers')
  await expect(triggers).toContainText('on_create')
  await expect(triggers).toContainText('created')
  await expect(triggers.getByRole('link', { name: 'main/report' })).toBeVisible()

  await page.getByTestId('tab-checks').click()
  await expect(page.getByTestId('schema-check')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Check documents against the schema' })).toBeVisible()
})

test('the API tab has snippets for each client built from the real address, and the realm\'s documents', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Data')
  await page.getByTestId('open-main-products').click()
  await page.getByTestId('tab-api').click()
  const source = page.getByTestId('fn-source')
  await expect(source).toContainText(`/v1/${REALM}/main/products`)
  // The code is shown whole, in a box that grows to it: no scroll area of its own.
  expect(await source.evaluate((el) => getComputedStyle(el).overflowY)).not.toBe('auto')
  for (const lang of ['JavaScript', 'Python', 'Go', 'Dart', 'curl']) {
    await page.getByRole('tab', { name: lang, exact: true }).click()
    await expect(source).toContainText('products')
  }
  // An enum field gives the example a real filter.
  await page.getByRole('tab', { name: 'curl', exact: true }).click()
  await expect(source).toContainText('kind')
  await expect(page.locator('main')).toContainText(`/v1/${REALM}/_openapi.json`)
})

test('a document has its tabs; the history lists the administrator\'s own changes', async ({ page, signIn }) => {
  const client = createClient({ url: base, realm: REALM })
  await client.auth.login({ email: users.admin, password: PASSWORD })
  const col = client.admin.data('main', 'products')
  const made = await col.create({ name: `Tabs ${Date.now()}`, price: 1 })
  await col.patch(made.id, { price: 2 })

  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/products/${made.id}`)
  await expect(page.getByTestId('doc-title')).toHaveText(made.id)
  // Files is a tab only for a collection with file fields (the `docs` one has them: files.spec covers it).
  await expect(page.getByRole('tab')).toHaveText(['Form', 'JSON', 'History'])
  await expect(page.getByTestId('doc-meta')).toBeVisible()

  await page.getByTestId('tab-json').click()
  await expect(page.locator('#doc-json')).toHaveValue(/"price": 2/)
  // History comes from the audit trail, which holds the changes made through the admin API.
  await page.getByTestId('tab-history').click()
  await expect(page.getByTestId('doc-history')).toBeVisible()
})

test('a versioned collection shows the schema version of each record, and how many are below the current one', async ({ page, signIn }) => {
  // scripts/ui-e2e.sh puts documents of versions 1, 2 and 3 in `assets` after its migration ran.
  await signIn('admin')
  await go(page, 'Data')
  await page.getByTestId('open-main-assets').click()
  await expect(page.getByRole('columnheader', { name: 'Schema version' })).toBeVisible()
  const versions = await page.getByTestId('schema-version-cell').allTextContents()
  expect(versions.sort()).toEqual(['1', '1', '1', '2', '2', '3', '3'])
  // A collection that is not versioned has no such column.
  await go(page, 'Data')
  await page.getByTestId('open-main-labels').click()
  await expect(page.getByRole('columnheader', { name: 'Schema version' })).toHaveCount(0)

  await go(page, 'Data')
  await page.getByTestId('open-main-assets').click()
  await page.getByTestId('tab-schema').click()
  await expect(page.getByText('Current version: 3')).toBeVisible()
  await expect(page.getByText('Documents still below the current version: 5')).toBeVisible()
  // The panel offers the dry run and the migration to an administrator who may write data.
  await expect(page.getByRole('button', { name: 'Migrate now' })).toBeVisible()
})

test('a level that cannot read functions is told so on the Triggers tab', async ({ page, signIn }) => {
  await signIn('support')
  // The support level has no data area either: the collection is out of reach.
  await page.goto(`/_ui/r/${REALM}/data/main/labels`)
  await expect(page.getByTestId('collection-name')).toHaveCount(0)
})
