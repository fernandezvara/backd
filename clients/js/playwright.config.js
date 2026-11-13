import { defineConfig } from '@playwright/test'

// The passkey test drives a real browser with a virtual authenticator against the integration
// stack (`make js-integration` starts it and runs this). PLAYWRIGHT_CHROMIUM points at a chromium
// binary when the downloaded one is not available.
export default defineConfig({
  testDir: './test/browser',
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: 'list',
  use: {
    launchOptions: process.env.PLAYWRIGHT_CHROMIUM ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM } : {},
  },
})
