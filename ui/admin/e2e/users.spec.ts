import { expect, test } from './fixtures'
import { PASSWORD, REALM, users } from './users'

const stamp = Date.now()
let n = 0
const fresh = () => `e2e-${stamp}-${++n}@adminui.example`

async function createUser(page: import('@playwright/test').Page, email: string, password = PASSWORD) {
  await page.getByRole('button', { name: 'Create user' }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Email').fill(email)
  await dialog.getByLabel('Password').fill(password)
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByTestId('user-email')).toHaveText(email)
}

test('a full administrator creates, finds and manages a user', async ({ page, signIn }) => {
  const email = fresh()
  await signIn('admin')
  await page.getByRole('link', { name: 'Users' }).click()
  await expect(page.getByTestId('users-table')).toContainText(users.admin)

  await createUser(page, email)
  await expect(page.getByTestId('user-status')).toHaveText('Active')

  // Search finds it from the list.
  await page.getByRole('link', { name: 'All users' }).click()
  await page.getByLabel('Search by email').fill(email)
  await expect(page.getByTestId('users-table').getByRole('row')).toHaveCount(2) // header + the user
  await page.getByRole('link', { name: email }).click()

  // Disable and enable.
  await page.getByRole('button', { name: 'Disable', exact: true }).click()
  await expect(page.getByTestId('user-status')).toHaveText('Disabled')
  await page.getByRole('button', { name: 'Enable', exact: true }).click()
  await expect(page.getByTestId('user-status')).toHaveText('Active')

  // Roles: the declared ones are offered; one that realm.yaml doesn't seed
  // for this user is flagged as living only in the database.
  await page.getByLabel('Role', { exact: true }).selectOption('editor')
  await page.getByRole('button', { name: 'Add role' }).click()
  await expect(page.getByTestId('user-roles')).toContainText('editor')
  await expect(page.getByTestId('user-roles')).toContainText('only in the database')
  await page.getByRole('button', { name: 'Remove editor' }).click()
  await expect(page.getByTestId('user-roles')).not.toContainText('editor')

  // Password and email.
  await page.getByRole('button', { name: 'Set password' }).click()
  await page.getByRole('dialog').getByLabel('New password').fill('another-p4ssw0rd!')
  await page.getByRole('dialog').getByRole('button', { name: 'Save' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Password changed' })).toBeVisible()
  // The realm sends no email, so changing an address (which tells both) isn't offered.
  await expect(page.getByRole('button', { name: 'Change email' })).toHaveCount(0)
})

test('sessions are listed and revoked', async ({ page, signIn, request }) => {
  const email = fresh()
  await signIn('admin')
  await page.getByRole('link', { name: 'Users' }).click()
  await createUser(page, email)
  const login = await request.post(`/v1/${REALM}/_auth/login`, { data: { email, password: PASSWORD } })
  expect(login.ok()).toBe(true)
  const { token } = await login.json()

  await page.reload() // the admin's session is in memory: reloading signs out, so sign in again
  await expect(page).toHaveURL(/signin/)
  await signIn('admin')
  await page.getByRole('link', { name: 'Users' }).click()
  await page.getByLabel('Search by email').fill(email)
  await page.getByRole('link', { name: email }).click()
  await expect(page.getByTestId('user-sessions').getByRole('row')).toHaveCount(2)
  await page.getByTestId('user-sessions').getByRole('button', { name: 'Revoke' }).click()
  await expect(page.getByText('No active sessions.')).toBeVisible()
  expect((await request.get(`/v1/${REALM}/_auth/me`, { headers: { Authorization: `Bearer ${token}` } })).status()).toBe(401)
})

test('erasing shows what the user owns and asks for the email', async ({ page, signIn }) => {
  const email = fresh()
  await signIn('admin')
  await page.getByRole('link', { name: 'Users' }).click()
  await createUser(page, email)
  await page.getByRole('button', { name: 'Delete user' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByTestId('owned-report')).toBeVisible()

  // Wrong text: nothing happens.
  await dialog.getByLabel(`Type ${email} to confirm`).fill('nope')
  await dialog.getByRole('button', { name: 'Erase user' }).click()
  await expect(dialog.getByText("The text doesn't match.")).toBeVisible()

  await dialog.getByLabel(`Type ${email} to confirm`).fill(email)
  await dialog.getByRole('button', { name: 'Erase user' }).click()
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/users$`))
  await page.getByLabel('Search by email').fill(email)
  await expect(page.getByText('No users found.')).toBeVisible()
})

test('invitations: create shows the token once, revoke needs the word', async ({ page, signIn }) => {
  await signIn('admin')
  await page.getByRole('link', { name: 'Invitations' }).click()
  await expect(page.getByRole('button', { name: 'Email an invitation' })).toHaveCount(0) // no email in this realm
  await page.getByRole('button', { name: 'Create invitation' }).click()
  await page.getByRole('dialog').getByLabel('Email').fill(fresh())
  await page.getByRole('dialog').getByRole('button', { name: 'Create' }).click()
  await expect(page.getByTestId('invitation-token')).toHaveValue(/^bdi_/)
  await page.getByRole('dialog').getByRole('button', { name: 'Done' }).click()
  await expect(page.getByTestId('invitations-table').getByRole('row')).not.toHaveCount(2)

  const rows = await page.getByTestId('invitations-table').getByRole('row').count()
  await page.getByTestId('invitations-table').getByRole('button', { name: 'Revoke' }).first().click()
  await page.getByRole('dialog').getByLabel('Type revoke to confirm').fill('revoke')
  await page.getByRole('dialog').getByRole('button', { name: 'Revoke invitation' }).click()
  await expect(page.getByTestId('invitations-table').getByRole('row')).toHaveCount(rows - 1)
})

test('a read-only administrator sees users with no control that changes anything', async ({ page, signIn }) => {
  await signIn('viewer')
  await expect(page.getByRole('link', { name: 'Users' })).toBeVisible()
  await page.getByRole('link', { name: 'Users' }).click()
  await expect(page.getByTestId('users-table')).toContainText(users.admin)
  await expect(page.getByRole('button', { name: 'Create user' })).toHaveCount(0)
  await page.getByRole('link', { name: users.admin }).click()
  await expect(page.getByTestId('user-email')).toHaveText(users.admin)
  await expect(page.getByTestId('user-actions')).toHaveCount(0)
  await expect(page.getByRole('button', { name: /^(Delete|Revoke|Add role|Remove)/ })).toHaveCount(0)
})

test('a custom level reaches only its areas', async ({ page, signIn }) => {
  await signIn('support')
  await expect(page.getByRole('link', { name: 'Users' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Invitations' })).toBeVisible()
  // Roles can still be added, by name: the configuration isn't readable at this level.
  await page.getByRole('link', { name: 'Users' }).click()
  await page.getByRole('link', { name: users.viewer }).click()
  await expect(page.getByLabel('Role', { exact: true })).toBeVisible()
  await expect(page.getByTestId('user-roles')).not.toContainText('only in the database')
})

test('the users and invitations views have no accessibility violations', async ({ page, signIn }) => {
  const { default: AxeBuilder } = await import('@axe-core/playwright')
  await signIn('admin')
  for (const theme of ['Light', 'Dark']) {
    await page.getByLabel('Theme').selectOption({ label: theme })
    await page.getByRole('link', { name: 'Users' }).click()
    await expect(page.getByTestId('users-table')).toContainText(users.admin)
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.getByRole('link', { name: users.viewer }).click()
    await expect(page.getByTestId('user-roles')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.getByRole('button', { name: 'Delete user' }).click()
    await expect(page.getByTestId('owned-report')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
    await page.keyboard.press('Escape')
    await page.getByRole('link', { name: 'Invitations' }).click()
    await expect(page.getByTestId('invitations-table')).toBeVisible()
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  }
})
