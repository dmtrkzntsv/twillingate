import { expect, test } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'

test('/api/docs opens Swagger UI, and Try it out is signed with the app login', async ({ page }) => {
  // Log in first, so the page has a refresh token to sign requests with.
  await page.goto('/app/dashboards')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/dashboards\/\d+/)

  await page.goto('/api/docs')
  await expect(page).toHaveURL(/\/api\/docs$/)
  await expect(page.getByRole('heading', { name: /Twillingate API/ })).toBeVisible()

  const op = page.locator('#operations-projects-list_projects')
  await op.locator('.opblock-summary').click()
  await op.getByRole('button', { name: 'Try it out' }).click()
  await op.getByRole('button', { name: 'Execute' }).click()
  await expect(op.locator('.live-responses-table tbody .response-col_status').first()).toHaveText('200')
  await expect(op.locator('.live-responses-table .microlight').first()).toContainText('"projects"')
})
