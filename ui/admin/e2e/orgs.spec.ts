import { createClient } from 'backd-js'
import { expect, test } from './fixtures'
import { PASSWORD, REALM, users } from './users'

const stamp = Date.now()

test('an administrator manages an organization: members, claims, invitations and deletion', async ({ page, signIn }) => {
  const name = `Acme ${stamp}`
  await signIn('admin')
  await page.getByRole('link', { name: 'Organizations' }).click()

  // Create it for a user of the realm, who becomes its owner.
  await page.getByRole('button', { name: 'Create organization' }).click()
  const create = page.getByRole('dialog')
  await create.getByLabel('Name').fill(name)
  await create.getByLabel("Owner's email").fill(users.admin)
  await create.getByRole('button', { name: 'Create', exact: true }).click()
  await expect(page.getByTestId('org-name')).toHaveText(name)
  await expect(page.getByTestId('org-status')).toHaveText('Active')
  await expect(page.getByTestId('org-members')).toContainText(users.admin)
  await expect(page.getByTestId('org-members')).toContainText('owner')

  // Add a member directly, then change their roles. The default role is held by everyone.
  await page.getByRole('button', { name: 'Add a member' }).click()
  const add = page.getByRole('dialog')
  await add.getByLabel('Email of the user').fill(users.plain)
  await add.getByLabel('Direct roles').fill('owner')
  await add.getByRole('button', { name: 'Create', exact: true }).click()
  const row = page.getByTestId('org-members').getByRole('row').filter({ hasText: users.plain })
  await expect(row).toContainText('owner')
  await expect(row).toContainText('default')
  await row.getByRole('button', { name: 'Change roles' }).click()
  const edit = page.getByRole('dialog')
  await edit.getByLabel('Direct roles').fill('editor')
  await edit.getByRole('button', { name: 'Save' }).click()
  await expect(row).toContainText('editor')

  // Claims are limited to 1 KiB.
  const claims = page.getByLabel('Claims (JSON object)')
  await claims.fill('{"plan": "pro"}')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Claims saved' })).toBeVisible()
  await claims.fill(JSON.stringify({ big: 'x'.repeat(1100) }))
  await expect(page.getByTestId('claims-size')).toContainText('bytes')
  await expect(page.getByRole('button', { name: 'Save' })).toBeDisabled()

  // An invitation: its link is shown once and never listed.
  await page.getByRole('button', { name: 'Invite' }).click()
  const invite = page.getByRole('dialog')
  await invite.getByLabel('Email of the user').fill('guest@adminui.example')
  await invite.getByRole('button', { name: 'Create', exact: true }).click()
  await expect(page.getByTestId('org-invitation-link')).toHaveValue(/bdo_/)
  await page.getByRole('dialog').getByRole('button', { name: 'Close' }).click()
  await expect(page.getByTestId('org-invitations')).toContainText('guest@adminui.example')
  await expect(page.getByTestId('org-invitations')).not.toContainText('bdo_')

  // Deleting shows what the cleanup does, and asks for the name.
  await page.getByRole('button', { name: 'Delete organization' }).click()
  const del = page.getByRole('dialog')
  await expect(del.getByTestId('org-owned')).toContainText('main/projects')
  await del.getByLabel(/Type/).fill(name)
  await del.getByRole('button', { name: 'Delete organization' }).click()
  await expect(page.getByTestId('org-status')).toHaveText('Deleted')

  // Deleted ones are found with the filter, and can come back.
  await page.getByRole('link', { name: 'Back to organizations' }).click()
  await page.getByLabel('Status').selectOption('deleted')
  await expect(page.getByTestId('orgs-table')).toContainText(name)
})

test('a read-only administrator sees organizations and has no controls', async ({ page, signIn }) => {
  await signIn('viewer')
  await page.getByRole('link', { name: 'Organizations' }).click()
  await expect(page.getByRole('button', { name: 'Create organization' })).toHaveCount(0)
})

test('an administrator gives and takes away a team role, with a window', async ({ page, signIn }) => {
  const name = `Departments ${stamp}`
  await signIn('admin', { keep: true })
  await page.getByRole('link', { name: 'Organizations' }).click()
  await page.getByRole('button', { name: 'Create organization' }).click()
  const create = page.getByRole('dialog')
  await create.getByLabel('Name').fill(name)
  await create.getByLabel("Owner's email").fill(users.admin)
  await create.getByRole('button', { name: 'Create', exact: true }).click()
  await expect(page.getByTestId('org-name')).toHaveText(name)
  const id = /\/orgs\/([^/?#]+)/.exec(page.url())![1]!

  // A member, and a team (made through the API: this view lists teams).
  await page.getByRole('button', { name: 'Add a member' }).click()
  const add = page.getByRole('dialog')
  await add.getByLabel('Email of the user').fill(users.plain)
  await add.getByRole('button', { name: 'Create', exact: true }).click()
  await expect(page.getByTestId('org-members')).toContainText(users.plain)
  const api = createClient({ url: process.env.E2E_URL ?? 'http://localhost:8080', realm: REALM })
  await api.auth.login({ email: users.admin, password: PASSWORD })
  await api.request({ method: 'POST', path: ['_admin', 'orgs', id, 'teams'], body: { name: 'Sales' } })
  await api.auth.logout()
  await page.reload()
  await expect(page.getByTestId('team-Sales')).toBeVisible()

  // A manager for a window that is open; the role needs a second factor, so the member is
  // listed as not having one yet.
  await page.getByTestId('team-Sales').getByRole('button', { name: 'Give a team role' }).click()
  const grant = page.getByRole('dialog')
  await grant.getByLabel('Email of the user').fill(users.plain)
  await grant.getByLabel('Team role').selectOption('manager')
  await grant.getByLabel('Until').fill('2099-01-01T00:00')
  await grant.getByRole('button', { name: 'Give a team role' }).click()
  const holder = page.getByTestId('team-Sales').getByTestId('team-role-holder')
  await expect(holder).toContainText(users.plain)
  await expect(holder).toContainText('manager')
  await expect(holder).toContainText('In force')
  await expect(holder).toContainText('not in the team')
  await expect(page.getByTestId('org-unenrolled')).toContainText(users.plain)

  // Taking it away asks for the member's name.
  await holder.getByRole('button', { name: 'Revoke' }).click()
  const revoke = page.getByRole('dialog')
  await revoke.getByLabel(/Type/).fill(users.plain)
  await revoke.getByRole('button', { name: 'Revoke' }).click()
  await expect(page.getByTestId('team-Sales').getByTestId('team-role-holder')).toHaveCount(0)
})

test('a read-only administrator sees team roles and has no controls for them', async ({ page, signIn }) => {
  await signIn('viewer')
  await page.getByRole('link', { name: 'Organizations' }).click()
  await expect(page.getByRole('button', { name: 'Give a team role' })).toHaveCount(0)
})
