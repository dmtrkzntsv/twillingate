import { expect, test, type Page } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'

async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/dashboards\/\d+/)
}

test('creates a project, edits it, manages a key, archives and restores it', async ({ page }) => {
  await login(page)
  await page.goto('/app/projects')
  await expect(page.getByRole('article', { name: 'dev' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Limits' })).toBeVisible()

  const name = `e2e-${Date.now()}`
  await page.getByRole('button', { name: 'New project' }).click()
  await page.getByLabel('Name').fill(name)
  await page.getByLabel('Allowed origins').fill('https://e2e.example')
  await page.getByLabel('Allowed origins').press('Enter')
  await page.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByText('Project created')).toBeVisible()
  await expect(page.getByText(/^ak_/)).toBeVisible()
  await page.keyboard.press('Escape')

  await page.getByRole('article', { name }).getByRole('link', { name }).click()
  await expect(page.getByRole('heading', { name })).toBeVisible()

  const details = page.getByRole('region', { name: 'Details' })
  await details.getByRole('button', { name: 'Edit' }).click()
  await page.getByLabel('Allowed origins').fill('*')
  await page.getByLabel('Allowed origins').press('Enter')
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

test('keeps the sidebar projects collapsed until opened', async ({ page }) => {
  await login(page)
  await expect(page.getByRole('link', { name: 'Projects' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'dev', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: 'Show projects' }).click()
  await expect(page.getByRole('link', { name: 'dev', exact: true })).toBeVisible()
})
