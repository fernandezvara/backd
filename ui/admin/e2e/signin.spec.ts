import AxeBuilder from '@axe-core/playwright'
import { expect, test } from './fixtures'
import { REALM, users } from './users'

test('a deep link to a protected page asks to sign in, then comes back', async ({ page, signIn }) => {
  await page.goto(`/_ui/r/${REALM}`)
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}/signin`))
  await expect(page.getByTestId('realm-badge')).toContainText(REALM)
  await signIn('admin')
  await expect(page).toHaveURL(new RegExp(`/_ui/r/${REALM}$`))
  await expect(page.getByTestId('realm-badge')).toContainText(`Realm ${REALM}`)
})

test('unknown /_ui/ paths fall back to the app, and the realm is validated', async ({ page }) => {
  await page.goto('/_ui/r/Bad_Realm!/anything')
  await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible()
})

test('the full administrator sees what whoami grants', async ({ page, signIn }) => {
  await signIn('admin')
  await expect(page.getByTestId('level')).toHaveText('Full administrator')
  await expect(page.getByTestId('can-write')).toContainText('Users')
  await expect(page.getByTestId('can-write')).toContainText('Configuration')
  await expect(page.getByText(users.admin, { exact: true }).first()).toBeVisible()
})

test('the read-only administrator can change nothing', async ({ page, signIn }) => {
  await signIn('viewer')
  await expect(page.getByTestId('level')).toHaveText('Read-only')
  await expect(page.getByTestId('can-write')).toHaveText('Nothing')
  await expect(page.getByTestId('can-read')).toContainText('Configuration')
  await expect(page.getByTestId('can-read')).toContainText('Users') // the realm's admin.read_access.users
  await expect(page.getByTestId('can-read')).toContainText('Data') // and admin.read_access.data
})

test('a custom level shows only its areas', async ({ page, signIn }) => {
  await signIn('support')
  await expect(page.getByTestId('level')).toHaveText('Custom')
  await expect(page.getByTestId('can-write')).toContainText('Users')
  await expect(page.getByTestId('can-write')).toContainText('Invitations')
  await expect(page.getByTestId('can-write')).not.toContainText('Secrets')
})

test('a wrong password and a user without an admin role are refused', async ({ page }) => {
  await page.goto(`/_ui/r/${REALM}/signin`)
  await page.getByLabel('Email').fill(users.admin)
  await page.getByLabel('Password').fill('not-the-password')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('alert')).toContainText('email or password is wrong')

  await page.getByLabel('Email').fill(users.plain)
  await page.getByLabel('Password').fill('dev-p4ssw0rd!')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByRole('alert')).toContainText('not an administrator')
  await expect(page).toHaveURL(/signin/)
})

test('the token stays in memory unless the tab opts in', async ({ page, signIn }) => {
  await signIn('admin')
  await expect(page.getByTestId('level')).toBeVisible()
  expect(await page.evaluate(() => JSON.stringify({ ...sessionStorage, ...localStorage }))).not.toContain('bds_')
  // A reload loses the session (and lands on the sign-in page).
  await page.reload()
  await expect(page).toHaveURL(/signin/)
})

test('opting in keeps the session across a reload, in this tab only', async ({ page, signIn }) => {
  await signIn('admin', { keep: true })
  await expect(page.getByTestId('level')).toBeVisible()
  expect(await page.evaluate(() => Object.keys(localStorage).filter((k) => k !== 'backd-admin:theme'))).toEqual([])
  await page.reload()
  await expect(page.getByTestId('level')).toBeVisible()
})

test('signing out revokes the session on the server', async ({ page, signIn, request }) => {
  const tokens: string[] = []
  page.on('request', (req) => {
    const auth = req.headers()['authorization']
    if (auth?.startsWith('Bearer ')) tokens.push(auth.slice(7))
  })
  await signIn('admin')
  await expect(page.getByTestId('level')).toBeVisible()
  await page.getByRole('button', { name: 'Sign out' }).click()
  await expect(page).toHaveURL(/signin/)
  const token = tokens.at(-1)!
  const res = await request.get(`/v1/${REALM}/_admin/whoami`, { headers: { Authorization: `Bearer ${token}` } })
  expect(res.status()).toBe(401)
})

test('the idle timeout signs out and revokes', async ({ page }) => {
  await page.clock.install()
  const tokens: string[] = []
  page.on('request', (req) => {
    const auth = req.headers()['authorization']
    if (auth?.startsWith('Bearer ')) tokens.push(auth.slice(7))
  })
  await page.goto(`/_ui/r/${REALM}/signin`)
  await page.getByLabel('Email').fill(users.admin)
  await page.getByLabel('Password').fill('dev-p4ssw0rd!')
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByTestId('level')).toBeVisible()
  await page.clock.fastForward('31:00')
  await expect(page).toHaveURL(/signin/)
  await expect(page.getByText('signed out after a period of inactivity')).toBeVisible()
  const check = await page.request.get(`/v1/${REALM}/_admin/whoami`, { headers: { Authorization: `Bearer ${tokens.at(-1)}` } })
  expect(check.status()).toBe(401)
})

test('the theme switch applies and the page has no accessibility violations', async ({ page, signIn }) => {
  await page.goto('/_ui/')
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await page.goto(`/_ui/r/${REALM}/signin`)
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  await signIn('admin')
  await expect(page.getByTestId('level')).toBeVisible()
  for (const theme of ['Light', 'Dark']) {
    await page.getByLabel('Theme').selectOption({ label: theme })
    await expect(page.locator('html')).toHaveClass(theme === 'Dark' ? /dark/ : /^(?!.*dark).*$/)
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([])
  }
})
