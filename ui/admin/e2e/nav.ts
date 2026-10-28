import { expect, type Page } from '@playwright/test'

// The pages are reached through the grouped menus: a group's button shows its links. These
// helpers work in any language, since they look for the label in every group.
const nav = (page: Page) => page.getByRole('navigation', { name: /^(Main|Principal)$/ })

const trigger = (page: Page, i: number) => nav(page).locator('[data-testid^="nav-group-"]').nth(i)

/** Opens one group and waits until its links are there. */
async function open(page: Page, i: number) {
  await trigger(page, i).click()
  await expect(trigger(page, i)).toHaveAttribute('aria-expanded', 'true')
  await nav(page).getByTestId('nav-label').first().waitFor()
}

/** Closes it and waits until it is closed, so the next one starts from a clean bar. */
async function close(page: Page, i: number) {
  await page.keyboard.press('Escape')
  await expect(trigger(page, i)).toHaveAttribute('aria-expanded', 'false')
}

/** Opens the menus until the link is found, then follows it. */
export async function go(page: Page, label: string) {
  const triggers = nav(page).locator('[data-testid^="nav-group-"]')
  const count = await triggers.count()
  for (let i = 0; i < count; i++) {
    await open(page, i)
    const link = nav(page).getByRole('link', { name: label, exact: true })
    if ((await link.count()) > 0) {
      await link.click()
      return
    }
    await close(page, i)
  }
  throw new Error(`no menu has the link ${JSON.stringify(label)}`)
}

/** The labels of every link in the menus, each group opened in turn. */
export async function navLabels(page: Page): Promise<string[]> {
  const triggers = nav(page).locator('[data-testid^="nav-group-"]')
  const labels: string[] = []
  const count = await triggers.count()
  for (let i = 0; i < count; i++) {
    await open(page, i)
    labels.push(...(await nav(page).getByTestId('nav-label').allTextContents()))
    await close(page, i)
  }
  return labels
}

// The account menu holds the language, the theme and the way out.
const accountButton = (page: Page) => page.getByTestId('account-menu')

/** Opens the account menu, runs the steps inside it and closes it again. */
export async function inAccount(page: Page, steps: () => Promise<void>) {
  await accountButton(page).click()
  await expect(accountButton(page)).toHaveAttribute('aria-expanded', 'true')
  await steps()
  await page.keyboard.press('Escape')
  await expect(accountButton(page)).toHaveAttribute('aria-expanded', 'false')
}

export const setTheme = (page: Page, label: string) => inAccount(page, () => page.getByLabel(/^(Theme|Tema)$/).selectOption({ label }))
export const setLanguage = (page: Page, value: string) => inAccount(page, () => page.getByTestId('language-switch').selectOption(value))

export async function signOut(page: Page) {
  await accountButton(page).click()
  await page.getByRole('button', { name: /^(Sign out|Cerrar sesión)$/ }).click()
}
