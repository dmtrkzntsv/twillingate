import { expect, test, type Page } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'

// Signs in from the console's address, which opens the projects.
async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)
}

test('creates a project, edits it, manages a key, archives and restores it', async ({ page }) => {
  await login(page)
  await page.goto('/app/projects')
  await expect(page.getByRole('article', { name: 'dev' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Limits' })).toBeVisible()

  const name = `e2e-${Date.now()}`
  await page.getByRole('button', { name: 'New project' }).click()
  await page.getByLabel('Name', { exact: true }).fill(name)
  await page.getByRole('button', { name: 'Add origin' }).click()
  await page.getByRole('textbox', { name: 'Origin 1' }).fill('https://e2e.example')
  await page.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByText('Project created')).toBeVisible()
  await expect(page.getByText(/^ak_/)).toBeVisible()
  await page.keyboard.press('Escape')

  await page.getByRole('article', { name }).getByRole('link', { name }).click()
  await expect(page.getByRole('heading', { name })).toBeVisible()

  const details = page.getByRole('region', { name: 'Details' })
  await details.getByRole('button', { name: 'Edit' }).click()
  await page.getByRole('button', { name: 'Add origin' }).click()
  await page.getByRole('textbox', { name: 'Origin 2' }).fill('*')
  await page.getByRole('button', { name: 'Save' }).click()
  await expect(details.getByText('*', { exact: true })).toBeVisible()

  const keys = page.getByRole('region', { name: 'Ingest keys' })
  await keys.getByRole('button', { name: 'Issue key' }).click()
  await page.getByLabel('Label').fill('ios app')
  await page.getByRole('button', { name: 'Issue', exact: true }).click()
  await expect(page.getByText('Key issued')).toBeVisible()
  await page.keyboard.press('Escape')
  await keys.getByRole('button', { name: 'Disable ios app' }).click()
  await page.getByRole('button', { name: 'Disable key' }).click()
  await expect(keys.getByRole('row', { name: /ios app/ }).getByText('disabled')).toBeVisible()

  await page.getByRole('button', { name: 'Archive', exact: true }).click()
  await page.getByRole('button', { name: 'Archive project' }).click()
  // The badge on the page; a toast says "Archived" too.
  await expect(page.getByRole('main').getByText('Archived', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Restore' }).click()
  await expect(page.getByRole('button', { name: 'Archive', exact: true })).toBeVisible()
})

test('shows usage and cap impact for the seeded project', async ({ page }) => {
  await login(page)
  await page.goto('/app/projects')
  await page.getByRole('article', { name: 'dev' }).getByRole('link', { name: 'dev' }).click()
  const usage = page.getByRole('region', { name: 'Usage' })
  await expect(usage.getByText('Events', { exact: true })).toBeVisible()
  await expect(usage.locator('.recharts-bar-rectangle').first()).toBeVisible()
  // cap_usage scans the dimension tables for the range: about 6s on the seeded 180 days.
  await expect(page.getByRole('region', { name: 'Cap impact' }).getByRole('row').nth(1)).toBeVisible({ timeout: 20_000 })
})

test('lists Projects first in the sidebar, as a plain link', async ({ page }) => {
  await login(page)
  const sidebar = page.locator('[data-sidebar="sidebar"]')
  await expect(sidebar.getByRole('link', { name: 'Projects' })).toHaveAttribute('href', '/app/projects')
  await expect(sidebar.getByRole('link', { name: 'dev', exact: true })).toHaveCount(0)
  await expect(sidebar.getByText('Dashboards', { exact: true })).toBeVisible()
  await expect(sidebar.getByRole('list', { name: 'Built-in dashboards' }).getByRole('link', { name: 'Views', exact: true })).toBeVisible()
})

test('adds a breakdown from the attributes the seeded project received, then removes it', async ({ page }) => {
  // Login, the wait below and the cleanup share one test limit, which must
  // outlast the wait's own 90s or the default 30s would cut it short.
  test.setTimeout(120_000)
  await login(page)
  await page.goto('/app/projects')
  await page.getByRole('article', { name: 'dev' }).getByRole('link', { name: 'dev' }).click()
  const breakdowns = page.getByRole('region', { name: 'Breakdowns' })
  // The pass that counts the seeded days runs after the server starts listening
  // (about 20s on a loaded host), so reopen the dialog, which refetches the keys,
  // until `plan` is listed. The wait is bounded by the toPass timeout (90s),
  // inside the test's 120s limit set above.
  const plan = page.getByRole('radio', { name: /plan/ })
  await expect(async () => {
    await breakdowns.getByRole('button', { name: 'Add breakdown' }).click()
    try {
      await expect(plan).toBeVisible({ timeout: 3_000 })
    } catch (e) {
      await page.keyboard.press('Escape')
      throw e
    }
  }).toPass({ timeout: 90_000, intervals: [2_000] })
  await plan.check()
  await page.getByRole('button', { name: 'Add', exact: true }).click()
  const row = breakdowns.getByRole('row', { name: /plan/ })
  await expect(row.getByText('plan', { exact: true })).toBeVisible()
  await expect(row.getByText(/events · \d+ values?/)).toBeVisible()
  // Leave the seed as it was for the other tests.
  await breakdowns.getByRole('button', { name: 'Remove plan' }).click()
  await page.getByRole('button', { name: 'Remove breakdown' }).click()
  await expect(breakdowns.getByRole('row', { name: /plan/ })).toHaveCount(0)
})
