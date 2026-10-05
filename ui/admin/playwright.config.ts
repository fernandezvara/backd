import { defineConfig } from '@playwright/test'

// Runs against a backd that serves the UI (BACKD_ADMIN_UI=true) with the
// adminui example realm loaded: `make ui-e2e` brings one up, or set E2E_URL.
// PLAYWRIGHT_CHROMIUM points at a chromium binary when the downloaded one
// isn't available.
export default defineConfig({
  testDir: './e2e',
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  globalSetup: './e2e/global-setup.ts',
  use: {
    baseURL: process.env.E2E_URL ?? 'http://localhost:8080',
    trace: 'retain-on-failure',
    launchOptions: process.env.PLAYWRIGHT_CHROMIUM ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM } : {},
  },
})
