import { expect, test as base } from '@playwright/test'
import { PASSWORD, REALM, users } from './users'

declare global {
  interface Window {
    __violations: string[]
  }
}

// Every test fails on a CSP violation or on a request to another origin:
// the interface loads nothing but its own files and talks only to backd.
export const test = base.extend<{ guard: void; signIn: (who: keyof typeof users, opts?: { keep?: boolean }) => Promise<void> }>({
  guard: [
    async ({ page, baseURL }, use) => {
      const origin = new URL(baseURL!).origin
      const foreign: string[] = []
      const consoleCSP: string[] = []
      await page.addInitScript(() => {
        window.__violations = []
        document.addEventListener('securitypolicyviolation', (e) => {
          window.__violations.push(`${e.violatedDirective} ${e.blockedURI}`)
        })
      })
      page.on('request', (req) => {
        const u = new URL(req.url())
        if (u.protocol !== 'blob:' && u.protocol !== 'data:' && u.origin !== origin) foreign.push(req.url())
      })
      page.on('console', (msg) => {
        if (/content security policy|Refused to/i.test(msg.text())) consoleCSP.push(msg.text())
      })
      await use()
      const seen = (await page.evaluate(() => window.__violations).catch(() => [] as string[])) ?? [] // a test that never opened a page has none
      expect(seen, 'CSP violations in the page').toEqual([])
      expect(consoleCSP, 'CSP messages in the console').toEqual([])
      expect(foreign, 'requests to another origin').toEqual([])
    },
    { auto: true },
  ],
  signIn: async ({ page }, use) => {
    await use(async (who, opts) => {
      await page.goto(`/_ui/r/${REALM}/signin`)
      await page.getByLabel('Email').fill(users[who])
      await page.getByLabel('Password').fill(PASSWORD)
      if (opts?.keep) await page.getByLabel('Keep me signed in in this tab').check()
      await page.getByRole('button', { name: 'Sign in' }).click()
      // Signed in once the shell is there: a full page load before this would drop the session.
      await page.getByRole('button', { name: 'Sign out' }).waitFor()
    })
  },
})

export { expect }
