import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { createProject } from './forms'

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

interface IngestResult {
  accepted: number
  rejected: number
  warnings?: { index: number; reason: string }[] | null
}

/** Posts product events named `names` (one each) from a server, as a backend would; a new key reaches the collector within a moment, so a 401 is retried. */
async function track(request: APIRequestContext, key: string, names: string[]): Promise<IngestResult> {
  let result: IngestResult | undefined
  await expect(async () => {
    const res = await request.post('/ingest/events', {
      headers: { 'X-Analytics-Key': key },
      data: {
        events: names.map((name) => ({ id: crypto.randomUUID(), ts: new Date().toISOString(), name, family: 'product' })),
      },
    })
    expect(res.status(), await res.text()).toBe(202)
    result = (await res.json()) as IngestResult
  }).toPass({ timeout: 15_000 })
  return result!
}

/** Waits for the write buffer to flush (every few seconds): the project's event names today, with how many each received. */
async function stored(request: APIRequestContext, id: number, want: Record<string, number>): Promise<void> {
  const today = new Date().toISOString().slice(0, 10)
  await expect
    .poll(
      async () => {
        const res = await request.get(`/api/projects/${id}/event-names?from=${today}&to=${today}`, { headers: { Authorization: `Bearer ${TOKEN}` } })
        expect(res.ok(), await res.text()).toBeTruthy()
        const { events } = (await res.json()) as { events: { name: string; events: number }[] }
        return Object.fromEntries(events.map((e) => [e.name, e.events]))
      },
      { timeout: 30_000 },
    )
    .toEqual(want)
}

test('blocks and renames events from a project settings page', async ({ page, request }) => {
  // A login, a wait for the write buffer's flush and a dozen UI steps.
  test.setTimeout(120_000)
  const { id, key } = await createProject(request, `events-${Date.now()}`, [])
  try {
    const first = await track(request, key, ['signup', 'signup', 'spam'])
    expect(first.accepted).toBe(3)
    await stored(request, id, { signup: 2, spam: 1 })

    await login(page)
    // The default range ends yesterday; these events are from today.
    await page.goto(`/app/projects/${id}/settings?range=today`)
    const events = page.getByRole('region', { name: 'Events' })
    const rows = events.getByRole('table')
    await expect(rows.getByRole('row', { name: /signup/ })).toContainText('2')
    await expect(rows.getByRole('row', { name: /spam/ })).toContainText('1')

    // Block spam: confirmed, marked on its row, and dropped on arrival.
    await events.getByRole('button', { name: 'Actions for spam (product)' }).click()
    await page.getByRole('menuitem', { name: 'Block…' }).click()
    await expect(page.getByRole('alertdialog', { name: 'Block spam?' })).toBeVisible()
    await page.getByRole('button', { name: 'Block event' }).click()
    await expect(page.getByText('Blocked spam')).toBeVisible()
    await expect(rows.getByRole('row', { name: /spam/ }).getByText('Blocked', { exact: true })).toBeVisible()
    await events.getByRole('button', { name: 'Actions for spam (product)' }).click()
    await expect(page.getByRole('menuitem', { name: 'Rename…' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Block…' })).toHaveCount(0)
    await page.keyboard.press('Escape')

    // Accepted, so a client does not retry, with a warning for whoever integrates.
    const again = await track(request, key, ['spam'])
    expect(again.accepted).toBe(1)
    expect(again.warnings?.map((w) => w.reason)).toEqual(['event "spam" is blocked in this project, dropped'])

    // Rename signup: its stored events move to the new name.
    await events.getByRole('button', { name: 'Actions for signup (product)' }).click()
    await page.getByRole('menuitem', { name: 'Rename…' }).click()
    await page.getByLabel('New name').fill('sign_up')
    await page.getByRole('button', { name: 'Rename', exact: true }).click()
    await expect(page.getByText('Renamed signup to sign_up')).toBeVisible()
    await expect(rows.getByRole('row', { name: /sign_up/ }).getByRole('button', { name: 'Remove alias signup' })).toBeVisible()
    await expect(rows.getByRole('row', { name: /sign_up/ })).toContainText('2')
    await expect(events.locator('code').filter({ hasText: /^signup$/ })).toHaveCount(0)

    // Unblock spam from its row, then remove the alias from under sign_up.
    await events.getByRole('button', { name: 'Actions for spam (product)' }).click()
    await page.getByRole('menuitem', { name: 'Unblock' }).click()
    await expect(page.getByText('Removed the rule for spam')).toBeVisible()
    await expect(events.getByText('Blocked', { exact: true })).toHaveCount(0)
    await events.getByRole('button', { name: 'Remove alias signup' }).click()
    await expect(page.getByText('Removed the rule for signup')).toBeVisible()
    await expect(events.getByRole('button', { name: 'Remove alias signup' })).toHaveCount(0)
  } finally {
    // Archived, so later specs' Projects page does not grow by one card per run.
    await request.post(`/api/projects/${id}/archive`, { headers: { Authorization: `Bearer ${TOKEN}` } })
  }
})
