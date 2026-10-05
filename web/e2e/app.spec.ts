import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'

const SYSTEM_TITLES = ['Views', 'Product', 'Users', 'Groups', 'Retention', 'Web Vitals', 'Measures']
const VIEWPORTS = [
  { width: 1280, height: 800, name: 'desktop' },
  { width: 390, height: 844, name: 'phone' },
]
// The error-card texts WidgetCard shows (see web/src/components/WidgetCard.tsx).
const ERROR_TEXTS = ['Query no longer runs', "Couldn't load", 'Component removed']

function authHeaders() {
  return { Authorization: `Bearer ${TOKEN}` }
}

/**
 * Logs in through the real password page (internal/api/oauth_page.html)
 * and waits for the app to land on a dashboard. `serve.sh`'s DSN turns on
 * the token:// browser login, so `/app/` bounces through the full OAuth
 * PKCE flow rather than skipping straight to "open" mode.
 */
async function login(page: Page): Promise<void> {
  await page.goto('/app/dashboards')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/dashboards\/\d+/)
  await page.waitForLoadState('networkidle')
}

/** Every dashboard's title -> id, read with the static API token directly (no browser). */
async function dashboardIds(request: APIRequestContext): Promise<Map<string, number>> {
  const res = await request.get('/api/dashboards', { headers: authHeaders() })
  expect(res.ok(), await res.text()).toBeTruthy()
  const body = (await res.json()) as { dashboards: { dashboard_id: number; title: string }[] }
  return new Map(body.dashboards.map((d) => [d.title, d.dashboard_id]))
}

async function assertNoErrorCards(page: Page): Promise<void> {
  for (const text of ERROR_TEXTS) {
    await expect(page.getByText(text), `found "${text}"`).toHaveCount(0)
  }
}

test.describe('login', () => {
  test('goes through the password page and lands on the Views report', async ({ page }) => {
    await login(page)
    await expect(page.getByRole('tab', { name: 'Views', exact: true })).toHaveAttribute('data-state', 'active')
  })
})

test.describe('system dashboards', () => {
  let ids: Map<string, number>

  test.beforeAll(async ({ request }) => {
    ids = await dashboardIds(request)
  })

  for (const title of SYSTEM_TITLES) {
    for (const viewport of VIEWPORTS) {
      test(`${title} renders at ${viewport.name}`, async ({ page }) => {
        await page.setViewportSize(viewport)
        await login(page)
        const id = ids.get(title)
        expect(id, `no dashboard titled ${title}`).toBeDefined()
        await page.goto(`/app/dashboards/${id}`)
        await page.waitForLoadState('networkidle')

        await assertNoErrorCards(page)
        // A real chart, not just a lucide icon: recharts' own surface class.
        await expect(page.locator('svg.recharts-surface').first()).toBeVisible()
      })
    }
  }
})

test('tabs carry the selection across to the next report', async ({ page }) => {
  await login(page)

  await page.getByRole('button', { name: /^Range:/ }).click()
  await page.getByRole('menuitemradio', { name: 'Last month' }).click()
  await expect(page).toHaveURL(/[?&]range=30d(&|$)/)

  await page.getByRole('tab', { name: 'Product', exact: true }).click()
  await expect(page).toHaveURL(/[?&]range=30d(&|$)/)
  await expect(page.getByRole('button', { name: 'Range: Last month' })).toBeVisible()
})

test('a lone user dashboard shows a tab bar of its one tab', async ({ page, request }) => {
  const created = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: { title: 'Standalone check', range: '7d' },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const { dashboard_id: id } = (await created.json()) as { dashboard_id: number }

  await login(page)
  await page.goto(`/app/dashboards/${id}`)
  await page.waitForLoadState('networkidle')

  // A lone dashboard is laid out like a group: a tab bar of its one tab.
  await expect(page.getByRole('tab')).toHaveText(['Standalone check'])
})

test('an agent-made group shows its tab bar', async ({ page, request }) => {
  const first = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: { title: 'Growth', range: '7d' },
  })
  expect(first.ok(), await first.text()).toBeTruthy()
  const { dashboard_id: firstId, group_id: groupId } = (await first.json()) as { dashboard_id: number; group_id: number }

  const second = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: { title: 'Retention (beta)', range: '7d', group_id: groupId },
  })
  expect(second.ok(), await second.text()).toBeTruthy()

  await login(page)
  await page.goto(`/app/dashboards/${firstId}`)
  await page.waitForLoadState('networkidle')

  const tablist = page.getByRole('tablist')
  await expect(tablist.getByRole('tab', { name: 'Growth', exact: true })).toBeVisible()
  await expect(tablist.getByRole('tab', { name: 'Retention (beta)', exact: true })).toBeVisible()
  // One sidebar entry for the whole group, not two.
  await expect(page.getByRole('link', { name: 'Growth', exact: true })).toHaveCount(1)
  await expect(page.getByRole('link', { name: 'Retention (beta)', exact: true })).toHaveCount(0)
})

test('a whole-group copy of Views opens with seven tabs', async ({ page, request }) => {
  const ids = await dashboardIds(request)
  const viewsId = ids.get('Views')
  expect(viewsId, 'no dashboard titled Views').toBeDefined()

  const copied = await request.post(`/api/dashboards/${viewsId}/duplicate`, {
    headers: authHeaders(),
    data: { whole_group: true },
  })
  expect(copied.ok(), await copied.text()).toBeTruthy()
  const { dashboard_id: copyId, tabs } = (await copied.json()) as { dashboard_id: number; tabs: { dashboard_id: number }[] }
  expect(tabs).toHaveLength(7)

  await login(page)
  await page.goto(`/app/dashboards/${copyId}`)
  await page.waitForLoadState('networkidle')

  await expect(page.getByRole('tab')).toHaveCount(7)
})

test('a 6x6 widget and four 3x3 widgets lay out as a 2x2 block beside it', async ({ page, request }) => {
  // Wide enough that WidgetGrid's span() keeps the authored widths verbatim
  // (>=1024px of grid width; see web/src/lib/grid.ts) even with the sidebar
  // open next to it.
  await page.setViewportSize({ width: 1440, height: 900 })

  const widget = (title: string, width: number, height: number) => ({
    component: 'markdown',
    title,
    source: { type: 'md', content: title },
    width,
    height,
  })
  const created = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: {
      title: 'Layout check',
      range: '7d',
      widgets: [
        widget('Main', 6, 6),
        widget('Q1', 3, 3),
        widget('Q2', 3, 3),
        widget('Q3', 3, 3),
        widget('Q4', 3, 3),
      ],
    },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const { dashboard_id: id } = (await created.json()) as { dashboard_id: number }

  await login(page)
  await page.goto(`/app/dashboards/${id}`)
  await page.waitForLoadState('networkidle')

  const card = (title: string) =>
    page.locator('[data-slot="widget-card"]').filter({ has: page.getByRole('heading', { name: title, exact: true }) })

  await expect(card('Main')).toBeVisible()
  await expect(card('Q4')).toBeVisible()
  const main = await card('Main').boundingBox()
  expect(main).not.toBeNull()

  for (const title of ['Q1', 'Q2', 'Q3', 'Q4']) {
    const box = await card(title).boundingBox()
    expect(box, `${title} has no box`).not.toBeNull()
    // Beside it: at or right of Main's own right edge.
    expect(box!.x).toBeGreaterThanOrEqual(main!.x + main!.width - 1)
    // Fits within Main's vertical extent (the 2x2 block is the same height).
    expect(box!.y).toBeGreaterThanOrEqual(main!.y - 1)
    expect(box!.y + box!.height).toBeLessThanOrEqual(main!.y + main!.height + 1)
  }
})

test('a table sorts by its header and remembers the sort across a reload', async ({ page, request }) => {
  const created = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: {
      title: 'Sort check',
      range: '7d',
      widgets: [
        {
          component: 'table',
          title: 'Scores',
          source: {
            type: 'sql',
            content: "SELECT 'b' AS name, 1 AS score UNION ALL SELECT 'a', 3 UNION ALL SELECT 'c', 2",
          },
          width: 6,
          height: 6,
        },
      ],
    },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const { dashboard_id: id } = (await created.json()) as { dashboard_id: number }

  await login(page)
  await page.goto(`/app/dashboards/${id}`)
  const names = page.locator('tbody tr td:first-child')
  await expect(names).toHaveText(['b', 'a', 'c'])

  await page.getByRole('button', { name: 'score', exact: true }).click()
  await expect(names).toHaveText(['a', 'c', 'b'])
  await expect(page.getByRole('columnheader', { name: 'score', exact: true })).toHaveAttribute('aria-sort', 'descending')

  // Kept by the card, with the filters, under the widget's own key.
  const stored = await page.evaluate(() =>
    Object.keys(localStorage)
      .filter((k) => k.startsWith('twillingate.widget.'))
      .map((k) => [k, localStorage.getItem(k)])
  )
  expect(stored).toEqual([[expect.stringMatching(/\.view$/), expect.stringContaining('"sort":{"column":"score","dir":"desc"}')]])

  await page.reload()
  await expect(names).toHaveText(['a', 'c', 'b'])
})

test('the attribute table filters and pages on the server', async ({ page, request }) => {
  const id = (await dashboardIds(request)).get('Product')
  expect(id, 'no Product dashboard').toBeDefined()
  await login(page)
  // The seed fills 180 days with two declared attributes, team_size (five
  // values) and role (three), so over all of them the table holds about 1,400
  // rows: two pages of CONSOLE_QUERY_MAX_ROWS (1,000). The table leaves out the
  // `$` keys. A `to` past today is cut to today by the server.
  const day = (n: number) => new Date(Date.now() + n * 86_400_000).toISOString().slice(0, 10)
  await page.goto(`/app/dashboards/${id}?range=custom&from=${day(-200)}&to=${day(1)}`)

  const card = page
    .locator('[data-slot=widget-card]')
    .filter({ has: page.getByRole('heading', { name: 'Attribute values by day', exact: true }) })
  await card.getByRole('button', { name: 'Filter', exact: true }).click()
  // The picker offers the column's values from the server, most frequent first.
  const options = page.getByRole('option')
  await expect(options.nth(1)).toBeVisible()
  const picked: string[] = []
  for (const i of [0, 1]) {
    picked.push((await options.nth(i).locator('span').first().textContent())!.trim())
    await options.nth(i).click()
  }
  await page.getByRole('button', { name: 'Apply', exact: true }).click()

  const chip = card.getByRole('button', { name: `Attribute in ${picked.join(', ')}`, exact: true })
  await expect(chip).toBeVisible()
  const attributes = card.locator('tbody tr td:first-child')
  // Every row on the page is one of the two picked: the server filtered the whole result.
  const onlyPicked = async () => {
    const shown = await attributes.allTextContents()
    return shown.length > 0 && shown.every((a) => picked.includes(a))
  }
  await expect.poll(onlyPicked).toBe(true)

  // Both attributes keep every row, more than one page.
  const footer = card.getByText(/^[\d,]+–[\d,]+ of [\d,]+$/)
  await expect(footer).toHaveText(/^1–1,000 of /)
  await card.getByRole('button', { name: 'Next page' }).click()
  await expect(footer).toHaveText(/^1,001–/)
  await expect.poll(onlyPicked).toBe(true)

  // The filters are stored and come back; the page is not, and starts again at 1.
  await page.reload()
  await expect(chip).toBeVisible()
  await expect.poll(onlyPicked).toBe(true)
  await expect(footer).toHaveText(/^1–1,000 of /)
})

for (const viewport of VIEWPORTS) {
  test(`a custom range stays open to pick, then applies at ${viewport.name}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    await login(page)
    await page.getByRole('button', { name: /^Range:/ }).click()
    await page.getByRole('menuitem', { name: 'Custom…' }).click()
    const apply = page.getByRole('button', { name: 'Apply' })
    await expect(apply).toBeVisible()
    // The menu's close animation used to take focus back and dismiss the picker.
    await page.waitForTimeout(500)
    await expect(apply).toBeVisible()
    const days = page.getByRole('grid').first().getByRole('button', { disabled: false })
    await days.nth(0).click()
    await days.nth(2).click()
    await apply.click()
    await expect(page).toHaveURL(/[?&]range=custom&from=\d{4}-\d\d-\d\d&to=\d{4}-\d\d-\d\d/)
  })
}
