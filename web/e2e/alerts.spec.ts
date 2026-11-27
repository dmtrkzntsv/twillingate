import { expect, test, type Page } from '@playwright/test'
import { createAlert, createProject } from './forms'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'

async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)
}

test('an alert is listed, edited, checked, snoozed, woken, archived and restored', async ({ page, request }) => {
  // A check is served on the runner's next tick, up to a minute after it is asked for.
  test.setTimeout(150_000)
  const { id } = await createProject(request, `alerts-${Date.now()}`, [])
  // The project never sent the event: the alert is made all the same, with a warning.
  const { id: alertId } = await createAlert(request, id, {
    name: 'Page views',
    template: 'event_count',
    fields: { event: '$page_view' },
    condition: { type: 'threshold', op: 'below', value: 1 },
  })

  await login(page)
  await page.goto(`/app/projects/${id}/settings`)
  await page.getByRole('link', { name: 'Alerts', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/alerts$`))
  // The row, pending or already checked.
  const row = page.getByRole('list', { name: 'Alerts' }).getByRole('link', { name: /^Page views/ })
  await expect(row).toContainText(/Pending|OK|Breached|No data/)

  await row.click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/alerts/${alertId}$`))
  await expect(page.getByRole('heading', { name: 'Page views' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Watches' })).toContainText('Count of $page_view')

  // A new threshold is a new definition: saving it asks for a check.
  const settings = page.getByRole('form', { name: 'Alert settings' })
  await settings.getByLabel('Threshold', { exact: true }).fill('5')
  await settings.getByRole('button', { name: 'Save', exact: true }).click()
  await expect(page.getByText('Saved', { exact: true })).toBeVisible()
  // The runner serves it on its next tick, within a minute; whatever it
  // finds, the check is then no longer pending. Read from the server, since
  // the page may show `Checking…` for a moment only, or not at all.
  await expect
    .poll(
      async () => {
        const res = await request.get(`/api/projects/${id}/alerts`, { headers: { Authorization: `Bearer ${TOKEN}` } })
        const alerts = ((await res.json()) as { alerts: { id: number; check_pending: boolean }[] }).alerts
        return alerts.find((a) => a.id === alertId)?.check_pending
      },
      { timeout: 90_000, intervals: [2_000] }
    )
    .toBe(false)
  // The page reads the list every 2 s while the check is pending, and follows.
  const checkNow = page.getByRole('button', { name: 'Check now' })
  await expect(checkNow).toBeVisible({ timeout: 10_000 })
  await expect(checkNow).not.toHaveAttribute('aria-disabled', 'true')
  await expect(page.getByRole('region', { name: 'History' }).locator('tbody tr').first()).toBeVisible({ timeout: 10_000 })

  // Snooze for a day, then wake.
  await page.getByRole('button', { name: 'Snooze', exact: true }).click()
  await page.getByRole('menuitem', { name: '1 day' }).click()
  await expect(page.getByText(/^Snoozed until /)).toBeVisible()
  await page.getByRole('button', { name: 'Wake', exact: true }).click()
  await expect(page.getByText(/^Snoozed until /)).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Snooze', exact: true })).toBeVisible()

  // Archive: the page stays, with Restore, and the alert moves to the Archived list.
  await page.getByRole('button', { name: 'Archive', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Restore', exact: true })).toBeVisible()
  const lists = page.getByRole('navigation', { name: 'Alert lists' })
  await expect(lists.getByRole('link', { name: 'Archived' })).toHaveAttribute('aria-current', 'page')
  await lists.getByRole('link', { name: 'Archived' }).click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/alerts\\?show=archived$`))
  await page.getByRole('list', { name: 'Alerts' }).getByRole('link', { name: /^Page views/ }).click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/alerts/${alertId}`))

  // Restore: live again, with its actions back.
  await page.getByRole('button', { name: 'Restore', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Archive', exact: true })).toBeVisible()
  await expect(lists.getByRole('link', { name: 'Live' })).toHaveAttribute('aria-current', 'page')
  await lists.getByRole('link', { name: 'Live' }).click()
  await expect(page.getByRole('list', { name: 'Alerts' }).getByRole('link', { name: /^Page views/ })).toBeVisible()
})
