import { createClient } from 'backd-js'
import { expect, test } from './fixtures'
import { PASSWORD, REALM, users } from './users'

const base = process.env.E2E_URL ?? 'http://localhost:8080'
const stamp = Date.now().toString(36)
let n = 0
const unique = () => `E2E ${stamp} ${++n}`

/** An admin client of the test's own, to change documents behind the UI's back. */
async function adminClient() {
  const client = createClient({ url: base, realm: REALM })
  await client.auth.login({ email: users.admin, password: PASSWORD })
  return client
}

test('databases and collections are listed and open', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await page.getByRole('link', { name: 'Data' }).click()
  await expect(page.getByTestId('open-main-products')).toContainText('keeps a trash')
  await expect(page.getByTestId('open-main-notes')).toBeVisible()
  await page.getByTestId('open-main-products').click()
  await expect(page.getByRole('heading', { name: 'products' })).toBeVisible()
  await expect(page.getByTestId('docs-table')).toContainText('Seed Widget')
})

test('the query builder offers only what the schema allows, and filters', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/products`)
  await expect(page.getByTestId('docs-table')).toContainText('Seed Widget')

  await page.getByRole('button', { name: 'Add condition' }).click()
  const row = page.getByTestId('condition').first()
  await row.getByLabel('Field').selectOption('name')
  // Text operators for a string, not for a number.
  await expect(row.getByLabel('Operator').locator('option', { hasText: 'contains (any case)' })).toHaveCount(1)
  await row.getByLabel('Field').selectOption('price')
  await expect(row.getByLabel('Operator').locator('option', { hasText: 'contains (any case)' })).toHaveCount(0)
  await expect(row.getByLabel('Operator').locator('option', { hasText: 'between' })).toHaveCount(1)

  await row.getByLabel('Field').selectOption('name')
  await row.getByLabel('Operator').selectOption('$icontains')
  await row.getByLabel('Value').fill('widg')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByTestId('total')).toHaveText('1 documents')
  await expect(page.getByTestId('docs-table')).toContainText('Seed Widget')
  await expect(page.getByTestId('docs-table')).not.toContainText('Seed Gadget')

  // An enum gets its values; a number is checked before anything is sent.
  await row.getByLabel('Field').selectOption('kind')
  await expect(row.getByLabel('Value').locator('option', { hasText: 'toy' })).toHaveCount(1)
  await row.getByLabel('Value').selectOption('toy')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByTestId('docs-table')).toContainText('Seed Gadget')
  await expect(page.getByTestId('total')).toHaveText('1 documents')

  await row.getByLabel('Field').selectOption('price')
  await row.getByLabel('Operator').selectOption('$gte')
  await row.getByLabel('Value').fill('abc')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'is not a number' })).toBeVisible()
  await row.getByLabel('Value').fill('20')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByTestId('docs-table')).toContainText('Seed Gadget') // 25

  // Sorting and the raw query.
  await page.getByRole('button', { name: 'Clear' }).click()
  await page.getByLabel('Sort by').selectOption('price')
  await page.getByLabel('Direction').selectOption('desc')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByTestId('docs-table').locator('tbody tr').first()).toContainText('Seed Bulk 45')
  await page.getByLabel('Write the query as JSON').check()
  await page.locator('#raw-where').fill('{"kind":"book"}')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByTestId('docs-table')).toContainText('Seed Novel')
  await expect(page.getByTestId('total')).toHaveText('1 documents')
})

test('paging walks all the documents', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/products`)
  await page.getByRole('button', { name: 'Add condition' }).click()
  const row = page.getByTestId('condition').first()
  await row.getByLabel('Field').selectOption('name')
  await row.getByLabel('Operator').selectOption('$startsWith')
  await row.getByLabel('Value').fill('Seed ')
  await page.getByLabel('Sort by').selectOption('name')
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByTestId('total')).toHaveText('48 documents')
  const rows = page.getByTestId('docs-table').locator('tbody tr')
  await expect(rows).toHaveCount(20)
  const first = (await rows.first().textContent()) ?? ''
  await page.getByRole('button', { name: 'Next' }).click()
  await expect(rows.first()).not.toHaveText(first)
  await page.getByRole('button', { name: 'Next' }).click()
  await expect(rows).toHaveCount(8)
  await expect(page.getByRole('button', { name: 'Next' })).toBeDisabled()
  await page.getByRole('button', { name: 'Previous' }).click()
  await page.getByRole('button', { name: 'Previous' }).click()
  await expect(rows.first()).toHaveText(first)
  await page.getByRole('button', { name: 'JSON', exact: true }).click()
  await expect(page.getByRole('region', { name: 'JSON' })).toContainText('"name": "Seed')
})

test('a document is created from a form, edited, and shows no owner', async ({ page, signIn }) => {
  const name = unique()
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/products`)
  await page.getByRole('link', { name: 'New document' }).click()

  // The schema's fields as controls: required marker, formats, limits, enum, object, array.
  await expect(page.locator('[data-path="name"] label')).toContainText('*')
  await expect(page.locator('[data-path="contact"] input')).toHaveAttribute('type', 'email')
  await expect(page.locator('[data-path="released"] input')).toHaveAttribute('type', 'datetime-local')
  await expect(page.locator('[data-path="price"]')).toContainText('minimum 0')
  await expect(page.locator('[data-path="name"]')).toContainText('at most 80 characters')
  await expect(page.locator('[data-path="extra"] textarea')).toBeVisible() // oneOf: a JSON editor

  await page.locator('[data-path="name"] input, [data-path="name"] textarea').fill(name)
  await page.locator('[data-path="price"] input').fill('-3')
  await page.locator('[data-path="kind"] select').selectOption('book')
  await page.getByRole('button', { name: 'Add to tags' }).click()
  await page.locator('[data-path="tags.0"] input, [data-path="tags.0"] textarea').fill('first')
  await page.getByRole('button', { name: 'Add to tags' }).click()
  await page.locator('[data-path="tags.1"] input, [data-path="tags.1"] textarea').fill('second')
  await page.getByRole('button', { name: 'Add dims' }).click()
  await page.locator('[data-path="dims.width"] input').fill('4.5')

  // The server's refusal lands next to the field.
  await page.getByRole('button', { name: 'Create', exact: true }).click()
  await expect(page.locator('[data-path="price"]')).toContainText(/minimum/)
  await expect(page.getByTestId('problems')).toContainText('price')

  await page.locator('[data-path="price"] input').fill('12.5')
  await page.getByRole('button', { name: 'Create', exact: true }).click()
  await expect(page.getByTestId('doc-version')).toHaveText('1')
  await expect(page.getByTestId('doc-meta')).toContainText('None') // no owner
  await expect(page.getByTestId('doc-meta')).toContainText('user:') // created_by

  // Edit: the form starts from the stored document; arrays reorder.
  await expect(page.locator('[data-path="tags.0"] input, [data-path="tags.0"] textarea')).toHaveValue('first')
  await page.getByRole('button', { name: 'Move tags 2 up' }).click()
  await expect(page.locator('[data-path="tags.0"] input, [data-path="tags.0"] textarea')).toHaveValue('second')
  await page.locator('[data-path="stock"] input').fill('7')
  await expect(page.getByText('Unsaved changes')).toBeVisible()
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByTestId('doc-version')).toHaveText('2')

  // The JSON view is the same document, and can be edited.
  await page.getByRole('button', { name: 'JSON', exact: true }).click()
  const json = page.locator('#doc-json')
  await expect(json).toHaveValue(/"stock": 7/)
  await json.fill((await json.inputValue()).replace('"stock": 7', '"stock": 8'))
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByTestId('doc-version')).toHaveText('3')
})

test('saving over a newer version shows it and lets the administrator choose', async ({ page, signIn }) => {
  const name = unique()
  const other = await adminClient()
  const made = await other.admin.data('main', 'products').create({ name, price: 1 })
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/products/${made.id}`)
  await expect(page.getByTestId('doc-version')).toHaveText('1')

  // Someone else changes it first.
  await other.admin.data('main', 'products').patch(made.id, { price: 2 })

  await page.locator('[data-path="stock"] input').fill('5')
  await page.getByRole('button', { name: 'Save' }).click()
  const conflict = page.getByTestId('conflict')
  await expect(conflict).toContainText('Someone changed this document')
  await expect(conflict).toContainText('version 2')
  await conflict.getByRole('button', { name: 'Show the newer version' }).click()
  await expect(conflict.getByRole('region')).toContainText('"price": 2')

  // Keep my changes: they save on top of the newer version.
  await conflict.getByRole('button', { name: /Keep my changes on top of version 2/ }).click()
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByTestId('doc-version')).toHaveText('3')
  await expect(page.locator('[data-path="stock"] input')).toHaveValue('5')

  // Or load the newer one and drop the edit.
  await other.admin.data('main', 'products').patch(made.id, { stock: 9 })
  await page.locator('[data-path="price"] input').fill('77')
  await page.getByRole('button', { name: 'Save' }).click()
  await page.getByTestId('conflict').getByRole('button', { name: /Load the newer version/ }).click()
  await expect(page.locator('[data-path="stock"] input')).toHaveValue('9')
  await expect(page.locator('[data-path="price"] input')).not.toHaveValue('77')
})

test('deleting goes to the trash, which can be browsed, restored and emptied', async ({ page, signIn }) => {
  const name = unique()
  const other = await adminClient()
  const made = await other.admin.data('main', 'products').create({ name })
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/products/${made.id}`)

  await page.getByRole('button', { name: 'Move to trash' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel(`Type ${made.id} to confirm`).fill('wrong')
  await dialog.getByRole('button', { name: 'Delete document' }).click()
  await expect(dialog.getByText("The text doesn't match.")).toBeVisible()
  await dialog.getByLabel(`Type ${made.id} to confirm`).fill(made.id)
  await dialog.getByRole('button', { name: 'Delete document' }).click()
  await expect(page).toHaveURL(new RegExp(`/data/main/products$`))

  // It is gone from the list and in the trash.
  await page.getByRole('button', { name: 'Trash' }).click()
  await expect(page.getByTestId('docs-table')).toContainText(made.id)
  await page.getByRole('button', { name: `Restore ${made.id}` }).click()
  await expect(page.getByTestId('docs-table')).not.toContainText(made.id)
  await page.getByRole('button', { name: 'Documents' }).click()
  await page.getByLabel('Write the query as JSON').check()
  await page.locator('#raw-where').fill(JSON.stringify({ id: made.id }))
  await page.getByRole('button', { name: 'Apply' }).click()
  await expect(page.getByTestId('docs-table')).toContainText(made.id)

  // Delete for good: from the trash only.
  await page.getByRole('link', { name: made.id }).click()
  await page.getByRole('button', { name: 'Move to trash' }).click()
  await page.getByRole('dialog').getByLabel(`Type ${made.id} to confirm`).fill(made.id)
  await page.getByRole('dialog').getByRole('button', { name: 'Delete document' }).click()
  await page.getByRole('button', { name: 'Trash' }).click()
  await page.getByRole('link', { name: made.id }).click()
  await expect(page.getByText('This document is in the trash')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Save' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Delete for good' }).click()
  await page.getByRole('dialog').getByLabel(`Type ${made.id} to confirm`).fill(made.id)
  await page.getByRole('dialog').getByRole('button', { name: 'Delete for good' }).click()
  await expect(page).toHaveURL(new RegExp(`/data/main/products$`))
  await page.getByRole('button', { name: 'Trash' }).click()
  await expect(page.getByTestId('docs-table')).not.toContainText(made.id)
})

test('a collection without a trash deletes for good', async ({ page, signIn }) => {
  const name = unique()
  await signIn('admin', { keep: true })
  await page.goto(`/_ui/r/${REALM}/data/main/labels`)
  await expect(page.getByRole('button', { name: 'Trash' })).toHaveCount(0)
  await page.getByRole('link', { name: 'New document' }).click()
  await page.locator('[data-path="name"] input, [data-path="name"] textarea').fill(name)
  await page.getByRole('button', { name: 'Create', exact: true }).click()
  await expect(page.getByTestId('doc-version')).toHaveText('1')
  await expect(page.getByRole('button', { name: 'Move to trash' })).toHaveCount(0)
  await page.getByRole('button', { name: 'Delete', exact: true }).click()
  const id = (await page.getByTestId('doc-meta').locator('dd').first().innerText()).trim()
  await page.getByRole('dialog').getByLabel(`Type ${id} to confirm`).fill(id)
  await page.getByRole('dialog').getByRole('button', { name: 'Delete document' }).click()
  await expect(page).toHaveURL(/\/data\/main\/labels$/)
  await expect(page.getByTestId('docs-table')).not.toContainText(id)
})

test('a read-only administrator browses with no way to change anything', async ({ page, signIn }) => {
  await signIn('viewer', { keep: true })
  await page.getByRole('link', { name: 'Data' }).click()
  await page.getByTestId('open-main-products').click()
  await expect(page.getByTestId('docs-table')).toContainText('Seed Widget')
  await expect(page.getByRole('link', { name: 'New document' })).toHaveCount(0)
  await page.getByRole('link', { name: /^[0-9a-v]{20}$/ }).first().click()
  await expect(page.getByText('You can read this document but not change it.')).toBeVisible()
  await expect(page.getByRole('button', { name: /^(Save|Create|Move to trash|Delete|Add)/ })).toHaveCount(0)
  await expect(page.locator('[data-path="stock"] input')).toHaveAttribute('readonly', '')
})

test('a custom level without the data area has no way in', async ({ page, signIn }) => {
  await signIn('support', { keep: true })
  await expect(page.getByRole('link', { name: 'Data' })).toHaveCount(0)
})

test('the data views have no accessibility violations', async ({ page, signIn }) => {
  const { default: AxeBuilder } = await import('@axe-core/playwright')
  const made = await (await adminClient()).admin.data('main', 'products').create({ name: unique(), tags: ['a'], dims: { width: 1 } })
  await signIn('admin', { keep: true })
  for (const theme of ['Light', 'Dark']) {
    await page.getByLabel('Theme').selectOption({ label: theme })
    await page.getByRole('link', { name: 'Data' }).click()
    await expect(page.getByTestId('open-main-products')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.getByTestId('open-main-products').click()
    await expect(page.getByTestId('docs-table')).toContainText('Seed Widget')
    await page.getByRole('button', { name: 'Add condition' }).click()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.goto(`/_ui/r/${REALM}/data/main/products/${made.id}`)
    await expect(page.getByTestId('doc-version')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.getByRole('button', { name: 'Move to trash' }).click()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.keyboard.press('Escape')
    await page.goto(`/_ui/r/${REALM}/data`)
  }
})
