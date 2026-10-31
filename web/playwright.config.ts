import { defineConfig, devices } from "@playwright/test"

// The smoke test of the panel runs against a master with a fake node, which TestPanelInBrowser in internal/e2e starts
// before it runs the test with the master's address and an administrator's account, see docs/development.md.
// CHROMIUM_PATH names another Chromium than the one `npx playwright install chromium` downloads.
export default defineConfig({
  testDir: "e2e",
  forbidOnly: !!process.env.CI,
  reporter: "list",
  use: {
    ...devices["Desktop Chrome"],
    baseURL: process.env.NORYX_PANEL_URL,
    ignoreHTTPSErrors: true, // the test's master has a certificate of its own
    launchOptions: { executablePath: process.env.CHROMIUM_PATH || undefined },
    trace: "retain-on-failure",
  },
})
