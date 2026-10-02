import { defineConfig, devices } from '@playwright/test'

const PORT = 18080
const BASE_URL = `http://127.0.0.1:${PORT}`

/**
 * Drives a seeded `twillingate serve` in a real Chromium browser: login,
 * every system dashboard, tab selection, the standalone shell and the
 * widget grid (see e2e/app.spec.ts). `webServer` builds and seeds a fresh
 * instance per run (e2e/serve.sh) and this config waits on its /healthz.
 *
 * e2e/arrange.spec.ts archives and restores the whole system group 1
 * (Reports) as part of its flow, which every other spec file assumes stays
 * live throughout (app.spec.ts's "Reports renders" tests, for one). With
 * `fullyParallel`, those files run concurrently against this one shared
 * server, so a mid-run archive would make them flaky. The `arrange`
 * project depends on `chromium` finishing first — Playwright runs a
 * dependency project to completion before starting the dependent one, even
 * under `fullyParallel` — so arrange.spec.ts only runs once nothing else
 * is touching the server; `chromium` excludes it via `testIgnore` so it is
 * not also run there. `npm run e2e` still exercises both projects.
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
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] }, testIgnore: 'e2e/arrange.spec.ts' },
    { name: 'arrange', use: { ...devices['Desktop Chrome'] }, testMatch: 'e2e/arrange.spec.ts', dependencies: ['chromium'] },
  ],
  webServer: {
    command: './e2e/serve.sh',
    url: `${BASE_URL}/healthz`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
    stdout: 'pipe',
    stderr: 'pipe',
    // SIGTERM, not the default SIGKILL, so serve.sh removes its scratch
    // directory (binary and database) on the way out.
    gracefulShutdown: { signal: 'SIGTERM', timeout: 10_000 },
  },
})
