import { expect, test } from './fixtures'
import { PASSWORD, REALM } from './users'
import { go, signOut } from './nav'

const stamp = Date.now()
const email = `e2e-pw-${stamp}@adminui.example`

// Expiring every password or ending every session would sign the other tests out of this shared
// realm, so these tests expire one user's password and only open the realm-wide dialogs.

test('an administrator expires one password, and a new one makes the account usable again', async ({ page, signIn, request }) => {
  await signIn('admin')
  await go(page, 'Users')
  await page.getByRole('button', { name: 'Create user' }).click()
  const create = page.getByRole('dialog')
  await create.getByLabel('Email').fill(email)
  await create.getByLabel('Password').fill(PASSWORD)
  await create.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByTestId('user-email')).toHaveText(email)
  await expect(page.getByTestId('user-status')).toHaveText('Active')

  // Only an administrator signs in at the realm's own login (people sign in to an organization), so this user gets an administrator role.
  await page.getByLabel('Role', { exact: true }).selectOption('viewer')
  await page.getByRole('button', { name: 'Add role' }).click()
  await expect(page.getByTestId('user-roles')).toContainText('viewer')

  await page.getByTestId('expire-password').click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Reason (for the audit trail)').fill('e2e')
  await dialog.getByRole('button', { name: 'Expire password' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Password expired.' })).toBeVisible()
  await expect(page.getByTestId('user-status')).toHaveText('Active · Password expired')

  // The right password is now refused with its own answer; a wrong one is still just wrong.
  const login = (password: string) => request.post(`/v1/${REALM}/_auth/login`, { data: { email, password } })
  const expired = await login(PASSWORD)
  expect(expired.status()).toBe(401)
  expect((await expired.json()).error.code).toBe('password_expired')
  expect((await (await login('not-the-p4ssw0rd!')).json()).error.code).toBe('invalid_credentials')

  // The list marks it.
  await page.getByRole('link', { name: 'All users' }).click()
  await page.getByLabel('Search by email').fill(email)
  await expect(page.getByRole('row', { name: email }).getByTestId('password-expired-tag')).toBeVisible()
  await page.getByRole('link', { name: email }).click()

  // A new password makes the account usable again.
  await page.getByRole('button', { name: 'Set password' }).click()
  await page.getByRole('dialog').getByLabel('New password').fill('another-p4ssw0rd!')
  await page.getByRole('dialog').getByRole('button', { name: 'Save' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Password changed' })).toBeVisible()
  await expect(page.getByTestId('user-status')).toHaveText('Active')
  expect((await login('another-p4ssw0rd!')).ok()).toBe(true)

  // The realm sends no email, so there is no reset link to make.
  await expect(page.getByTestId('reset-link')).toHaveCount(0)
})

test('a user whose password expired replaces it on the sign-in page with the current one', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Users')
  await page.getByLabel('Search by email').fill(email)
  await page.getByRole('link', { name: email }).click()
  await page.getByTestId('expire-password').click()
  await page.getByRole('dialog').getByRole('button', { name: 'Expire password' }).click()
  await expect(page.getByTestId('user-status')).toHaveText('Active · Password expired')
  await signOut(page)

  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill('another-p4ssw0rd!')
  await page.getByRole('button', { name: 'Sign in' }).click()
  const form = page.getByTestId('expired-step')
  await expect(form).toContainText('This password has expired')
  expect((await new (await import('@axe-core/playwright')).default({ page }).analyze()).violations).toEqual([])

  await form.getByLabel('Current password').fill('not-the-p4ssw0rd!')
  await form.getByLabel('New password', { exact: true }).fill('renewed-p4ssw0rd!')
  await form.getByLabel('New password again').fill('renewed-p4ssw0rd!')
  await form.getByRole('button', { name: 'Change password' }).click()
  await expect(page.getByText('The current password is not right.')).toBeVisible()

  await form.getByLabel('Current password').fill('another-p4ssw0rd!')
  await form.getByLabel('New password again').fill('different-p4ssw0rd!')
  await form.getByRole('button', { name: 'Change password' }).click()
  await expect(form.getByText('The two new passwords are not the same.')).toBeVisible()

  await form.getByLabel('New password again').fill('renewed-p4ssw0rd!')
  await form.getByRole('button', { name: 'Change password' }).click()
  await expect(page.getByTestId('password-changed')).toBeVisible()

  // The new password signs in.
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill('renewed-p4ssw0rd!')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await page.getByRole('navigation', { name: 'Main' }).waitFor()
})

test('the actions for a leak ask for the number of accounts and the realm name', async ({ page, signIn }) => {
  await signIn('admin')
  await go(page, 'Users')
  const panel = page.getByTestId('passwords-panel')
  await expect(panel).toHaveCount(0) // folded until asked for
  await page.getByTestId('passwords-toggle').click()
  await expect(panel).toBeVisible()
  await expect(page.getByTestId('passwords-status')).toContainText('accounts have a password')

  await panel.getByRole('button', { name: 'Expire every password' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByLabel('Email every user a link to choose a new password')).toHaveCount(0) // no email in this realm
  await dialog.getByLabel(/^Type \d+ to confirm$/).fill('1')
  await dialog.getByLabel('Your password').fill(PASSWORD)
  await dialog.getByRole('button', { name: 'Expire every password' }).click()
  await expect(dialog.getByText(/Type the number of accounts/)).toBeVisible()
  await dialog.getByRole('button', { name: 'Cancel' }).click()

  await panel.getByRole('button', { name: 'End every session' }).click()
  const revoke = page.getByRole('dialog')
  await revoke.getByLabel(`Type ${REALM} to confirm`).fill('nope')
  await revoke.getByLabel('Your password').fill(PASSWORD)
  await revoke.getByRole('button', { name: 'End every session' }).click()
  await expect(revoke.getByText(/Type the realm name/)).toBeVisible()
  await revoke.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
})

test('only a full administrator sees the actions', async ({ page, signIn }) => {
  await signIn('support')
  await go(page, 'Users')
  await expect(page.getByTestId('users-table')).toBeVisible()
  await expect(page.getByTestId('passwords-panel')).toHaveCount(0)
  await expect(page.getByTestId('passwords-toggle')).toHaveCount(0)
})
