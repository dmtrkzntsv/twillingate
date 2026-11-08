import { expect, test, type Page } from '@playwright/test'
import { createProject, submitJSON } from './forms'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'

async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)
}

// Archive all empties the whole inbox, every spec's items with it, so this
// file runs in its own project after the others (playwright.config.ts).
test('a new form draft shows in the inbox, and Archive all empties it and the badge', async ({ page, request }) => {
  test.setTimeout(60_000)
  // The first submission to an unknown form creates its draft, and the draft a form_draft_created item.
  const { id, key } = await createProject(request, `inbox-${Date.now()}`, [])
  await submitJSON(request, key, 'contact', { email: 'ann@example.com', message: 'Hello' })

  await login(page)
  await page.goto('/app/inbox')
  const item = page.getByRole('listitem', { name: 'New form contact is waiting for approval' }).filter({ hasText: `inbox-` })
  await expect(item.first()).toBeVisible()
  await expect(item.first().getByRole('link', { name: 'New form contact is waiting for approval' })).toHaveAttribute('href', `/app/projects/${id}/forms/contact`)
  const badge = page.getByTitle(/^\d+ active notifications?$/)
  await expect(badge).toBeVisible()

  await page.getByRole('button', { name: 'Archive all' }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Archive all' }).click()
  await expect(page.getByText('Nothing in your inbox')).toBeVisible()
  await expect(badge).toHaveCount(0)

  // It waits on the Archived tab, to restore.
  await page.getByRole('tab', { name: 'Archived' }).click()
  await expect(page).toHaveURL(/\/app\/inbox\?show=archived$/)
  await expect(item.first()).toBeVisible()
})
