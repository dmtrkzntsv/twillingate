import { expect, test, type Page } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'
const headers = { Authorization: `Bearer ${TOKEN}` }

async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)
}

const day = (d: Date) => d.toISOString().slice(0, 10)

test('a selector narrows the widgets that follow it, through the URL', async ({ page, request }) => {
  test.setTimeout(90_000)
  // A Deployment selector over the browser column (the seed varies it), a
  // stat that follows it, and a note that follows nothing, so the page
  // loads its widgets through the stream.
  const title = `Selectors ${Date.now()}`
  const stat = JSON.stringify({ query: { kind: 'stat', table: 'views', metrics: [{ op: 'count' }] }, rename: { count: 'value' } })
  const created = await request.post('/api/dashboards', {
    headers,
    data: {
      title,
      selectors: [{ name: 'deployment', label: 'Deployment', column: 'browser' }],
      widgets: [
        { component: 'stat', title: 'Views', source: { type: 'query', content: stat }, width: 6, height: 4 },
        { component: 'markdown', title: 'Note', source: { type: 'md', content: 'Not filtered.' }, width: 6, height: 4 },
      ],
    },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const { dashboard_id: id } = (await created.json()) as { dashboard_id: number }
  try {
    // A value of the column around the range the page shows (the last
    // month): with more than one browser, any one has fewer views than all.
    const now = Date.now()
    const from = day(new Date(now - 40 * 86_400_000))
    const values = await request.get(`/api/dashboards/${id}/selectors/deployment/values?project_id=1&from=${from}&to=${day(new Date(now))}`, {
      headers,
    })
    expect(values.ok(), await values.text()).toBeTruthy()
    const listed = ((await values.json()) as { values: { value: string; count: number }[] }).values
    expect(listed.length, 'the seed has more than one browser').toBeGreaterThan(1)
    const value = listed[0].value

    await login(page)
    await page.goto(`/app/dashboards/${id}?project=1&range=30d`)
    const card = page.locator('[data-slot="widget-card"]').filter({ has: page.getByRole('heading', { name: 'Views', exact: true }) })
    await expect(card.getByText(/\d/).first()).toBeVisible()
    await expect(card.locator('[data-slot=skeleton]')).toHaveCount(0)
    const all = await card.innerText()

    await page.getByRole('button', { name: 'Deployment: All' }).click()
    await page.getByRole('checkbox', { name: value, exact: true }).click()
    await expect.poll(() => new URL(page.url()).searchParams.getAll('s.deployment')).toEqual([value])
    await expect(page.getByRole('button', { name: `Deployment: ${value}` })).toBeVisible()
    await page.keyboard.press('Escape')
    await expect.poll(() => card.innerText(), { timeout: 20_000 }).not.toBe(all)
    await expect(card.locator('[data-slot=skeleton]')).toHaveCount(0)
    const narrowed = await card.innerText()
    // The note follows nothing, so its card says the pick does not filter it; the stat follows, so its card does not.
    await expect(page.getByText('Not filtered by Deployment')).toHaveCount(1)
    await expect(card.getByText(/Not filtered by/)).toHaveCount(0)

    // Reloaded, the URL keeps the selection, and the stat its narrowed answer.
    await page.reload()
    await expect(page.getByRole('button', { name: `Deployment: ${value}` })).toBeVisible()
    await expect.poll(() => card.innerText(), { timeout: 20_000 }).toBe(narrowed)

    // Opened from the sidebar, the dashboard starts at All.
    await page.getByRole('link', { name: title, exact: true }).click()
    await expect.poll(() => new URL(page.url()).searchParams.has('s.deployment')).toBe(false)
    expect(new URL(page.url()).pathname).toBe(`/app/dashboards/${id}`)
    await expect(page.getByRole('button', { name: 'Deployment: All' })).toBeVisible()
    await expect(page.getByText(/Not filtered by/)).toHaveCount(0)
  } finally {
    await request.post(`/api/dashboards/${id}/archive`, { headers, data: {} })
  }
})
