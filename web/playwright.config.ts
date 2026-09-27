import { defineConfig, devices } from '@playwright/test'

const PORT = 18080
const BASE_URL = `http://127.0.0.1:${PORT}`

/**
 * Drives a seeded `twillingate serve` in a real Chromium browser: login,
 * every system dashboard, tab selection, the standalone shell and the
 * widget grid (see e2e/app.spec.ts). `webServer` builds and seeds a fresh
 * instance per run (e2e/serve.sh) and this config waits on its /healthz.
 */
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? 'line' : 'list',
  use: {
    baseURL: BASE_URL,
    viewport: { width: 1280, height: 800 },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
  webServer: {
    command: './e2e/serve.sh',
    url: `${BASE_URL}/healthz`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
})
