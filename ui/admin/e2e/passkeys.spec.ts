import { createClient } from 'backd-js'
import type { BrowserContext, Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { PASSWORD } from './users'

// The passkeys example realm (examples/config/passkeys/realm.yaml): an administrator bootstrapped by scripts/ui-e2e.sh.
const REALM = 'passkeys'
const ADMIN = 'admin@passkeys.example'
import { signOut } from './nav'

// Passkeys in the admin interface, with Chromium's virtual authenticator (a CTAP2 device with a resident key and user
// verification): add one, sign in with it, use it as a later step, remove it from the account page, and the administrator's
// view of someone else's. Each test has its own user, an operations-level administrator.

const stamp = Date.now()
const base = process.env.E2E_URL ?? 'http://localhost:8080'

async function makeUser(name: string) {
  const email = `e2e-pk-${name}-${stamp}@adminui.example`
  const admin = createClient({ url: base, realm: REALM })
  await admin.auth.login({ email: ADMIN, password: PASSWORD, strategy: 'password' })
  await admin.admin.users.create({ email, password: PASSWORD })
  const made = await admin.admin.users.find(email)
  await admin.admin.users.addRole(made!.id, 'viewer')
  await admin.auth.logout()
  return { email, id: made!.id }
}

async function authenticator(context: BrowserContext, page: Page) {
  const cdp = await context.newCDPSession(page)
  await cdp.send('WebAuthn.enable')
  const { authenticatorId } = await cdp.send('WebAuthn.addVirtualAuthenticator', {
    options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true },
  })
  // The sign-in page offers the passkeys in the email field's autofill, and the virtual authenticator answers at once,
  // signing the person in: to test a password first, the keys are taken out of the authenticator and put back for the step.
  type Held = Awaited<ReturnType<typeof getCredentials>>
  const getCredentials = async () => (await cdp.send('WebAuthn.getCredentials', { authenticatorId })).credentials
  let held: Held = []
  return {
    async hide() {
      held = await getCredentials()
      for (const c of held) await cdp.send('WebAuthn.removeCredential', { authenticatorId, credentialId: c.credentialId })
    },
    async restore() {
      for (const credential of held) await cdp.send('WebAuthn.addCredential', { authenticatorId, credential })
    },
  }
}

async function signInWithPassword(page: Page, email: string, strategy?: string) {
  await page.goto(`/_ui/r/${REALM}/signin`)
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(PASSWORD)
  // (Kept in this tab: the tests open pages by address, and a session held only in memory would not survive that.)
  await page.getByLabel('Keep me signed in in this tab').check()
  if (strategy) await page.getByTestId('strategy-field').selectOption(strategy)
  await page.getByRole('button', { name: 'Sign in', exact: true }).click()
}

const shell = (page: Page) => page.getByRole('navigation', { name: /^(Main|Principal)$/ })

async function addPasskey(page: Page, name: string) {
  await page.goto(`/_ui/r/${REALM}/account`)
  await page.getByTestId('passkey-add').click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name').fill(name)
  await dialog.getByLabel('Your password').fill(PASSWORD)
  await dialog.getByRole('button', { name: 'Add', exact: true }).click()
  await expect(page.getByTestId('passkey-row').filter({ hasText: name })).toBeVisible()
}

test('add a passkey, sign in with it, use it as a later step, and remove it', async ({ page, context }) => {
  const { email } = await makeUser('flow')
  const device = await authenticator(context, page)

  await signInWithPassword(page, email, 'password')
  await shell(page).waitFor()
  await addPasskey(page, 'Laptop')
  await expect(page.getByTestId('passkeys-table')).toContainText('Laptop')

  // The passkey alone signs in: no email, no password.
  await signOut(page)
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  await page.goto(`/_ui/r/${REALM}/signin`)
  // The virtual authenticator answers the browser's autofill request (conditional UI) at once, so the page usually signs the
  // person in by itself, taking the button away; when it has not yet, the button does the same.
  await page.getByTestId('passkey-signin-button').click({ timeout: 3000 }).catch(() => {})
  await shell(page).waitFor()

  // A strategy with the password and then the passkey.
  await device.hide()
  await signOut(page)
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  await signInWithPassword(page, email, 'password_passkey')
  await expect(page.getByTestId('passkey-step')).toBeVisible()
  await device.restore()
  await page.getByTestId('passkey-step-use').click()
  await shell(page).waitFor()

  // Removing it asks for the passkey (the only proof this account gives now), and ends the session it opened.
  await page.goto(`/_ui/r/${REALM}/account`)
  await expect(page.getByTestId('passkey-row')).toHaveCount(1)
  await page.getByRole('button', { name: 'Delete Laptop' }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByTestId('proof-passkey')).toBeVisible()
  await dialog.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
})

test('an administrator sees and removes the passkey of a lost device', async ({ page, context, browser }) => {
  const { email, id } = await makeUser('lost')

  // The person adds one in their own browser.
  const theirs = await browser.newContext({ ignoreHTTPSErrors: true })
  const theirPage = await theirs.newPage()
  await theirPage.goto(`/_ui/r/${REALM}/signin`, { waitUntil: 'commit' }).catch(() => {})
  await authenticator(theirs, theirPage)
  await signInWithPassword(theirPage, email, 'password')
  await shell(theirPage).waitFor()
  await addPasskey(theirPage, 'Lost phone')

  await authenticator(context, page)
  await signInWithPassword(page, ADMIN, 'password')
  await shell(page).waitFor()
  await page.goto(`/_ui/r/${REALM}/users/${id}`)
  await expect(page.getByTestId('user-passkeys')).toContainText('Lost phone')
  await page.getByRole('button', { name: 'Remove Lost phone' }).click()
  await page.getByRole('dialog').getByLabel('Type Lost phone to confirm').fill('Lost phone')
  await page.getByRole('dialog').getByRole('button', { name: 'Remove', exact: true }).click()
  await expect(page.getByTestId('user-passkeys-none')).toBeVisible()
  await theirs.close()
})
