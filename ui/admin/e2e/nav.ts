import { expect, type Page } from '@playwright/test'

// The pages are reached through the sidebar: groups of links that are always there on a wide screen,
// behind the menu button on a narrow one. These helpers work in any language, since they look for the
// label in the whole menu.
const nav = (page: Page) => page.getByRole('navigation', { name: /^(Main|Principal)$/ })

/** On a narrow screen the sidebar is a drawer: opens it when it is closed. */
async function openSidebar(page: Page) {
  const toggle = page.getByTestId('nav-toggle')
  // The button exists on every screen but is only shown on a narrow one.
  if ((await toggle.isVisible()) && (await toggle.getAttribute('aria-expanded')) === 'false') await toggle.click()
  await expect(nav(page)).toBeVisible()
}

/** Follows the link with this label. */
export async function go(page: Page, label: string) {
  await openSidebar(page)
  const link = nav(page).getByRole('link', { name: label, exact: true })
  if ((await link.count()) === 0) throw new Error(`the menu has no link ${JSON.stringify(label)}`)
  await link.click()
}

/** The labels of every link of the menu. */
export async function navLabels(page: Page): Promise<string[]> {
  await openSidebar(page)
  await nav(page).getByTestId('nav-label').first().waitFor({ state: 'attached' })
  return nav(page).getByTestId('nav-label').allTextContents()
}

// The account menu holds who is signed in, the link to the Account page and the way out.
const accountButton = (page: Page) => page.getByTestId('account-menu')

/** Opens the account menu, runs the steps inside it and closes it again. */
export async function inAccount(page: Page, steps: () => Promise<void>) {
  await accountButton(page).click()
  await expect(accountButton(page)).toHaveAttribute('aria-expanded', 'true')
  await steps()
  await page.keyboard.press('Escape')
  await expect(accountButton(page)).toHaveAttribute('aria-expanded', 'false')
}

/** Opens the Account page, where the appearance is chosen. */
export async function openAccount(page: Page) {
  if (await page.getByTestId('preferences').isVisible()) return
  await accountButton(page).click()
  await page.getByTestId('account-link').click()
  await expect(page.getByTestId('preferences')).toBeVisible()
}

/**
 * Chooses the theme by its label. The sign-in page has the switch itself; inside the shell the top
 * bar's button goes round system, light and dark, so it is pressed until it names the one asked for
 * (the page stays where it is).
 */
export async function setTheme(page: Page, label: string) {
  const radio = page.getByRole('radio', { name: label, exact: true })
  if ((await radio.count()) > 0) {
    await radio.click()
    return
  }
  const toggle = page.getByTestId('theme-toggle')
  for (let i = 0; i < 3; i++) {
    if (((await toggle.getAttribute('aria-label')) ?? '').endsWith(`: ${label}`)) return
    await toggle.click()
  }
  throw new Error(`the theme button never reached ${JSON.stringify(label)}`)
}

/** Chooses the language on the Account page ('auto', 'en' or 'es'); the page stays there. */
export async function setLanguage(page: Page, value: string) {
  await openAccount(page)
  await page.getByTestId(`language-${value}`).click()
  await expect(page.getByTestId(`language-${value}`)).toHaveAttribute('aria-checked', 'true')
}

export async function signOut(page: Page) {
  await accountButton(page).click()
  await page.getByTestId('sign-out').click()
}
