import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

// Matches web/e2e/serve.sh's API_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'

const SYSTEM_TITLES = ['Views', 'Product', 'Users', 'Groups', 'Retention']
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
  await page.goto('/app/')
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

test('a user dashboard opens in the standalone shell with no report tabs', async ({ page, request }) => {
  const created = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: { title: 'Standalone check', range: '7d' },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const { dashboard_id: id } = (await created.json()) as { dashboard_id: number }

  await login(page)
  await page.goto(`/app/dashboards/${id}`)
  await page.waitForLoadState('networkidle')

  await expect(page.getByRole('tablist')).toHaveCount(0)
  // The sidebar also has a "Yours" section label; scope to the main pane's
  // header, where DashboardView puts the owner label in place of ReportTabs.
  await expect(page.getByRole('main').getByText('Yours', { exact: true })).toBeVisible()
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

test('a whole-group copy of Views opens with five tabs', async ({ page, request }) => {
  const ids = await dashboardIds(request)
  const viewsId = ids.get('Views')
  expect(viewsId, 'no dashboard titled Views').toBeDefined()

  const copied = await request.post(`/api/dashboards/${viewsId}/duplicate`, {
    headers: authHeaders(),
    data: { whole_group: true },
  })
  expect(copied.ok(), await copied.text()).toBeTruthy()
  const { dashboard_id: copyId, tabs } = (await copied.json()) as { dashboard_id: number; tabs: { dashboard_id: number }[] }
  expect(tabs).toHaveLength(5)

  await login(page)
  await page.goto(`/app/dashboards/${copyId}`)
  await page.waitForLoadState('networkidle')

  await expect(page.getByRole('tab')).toHaveCount(5)
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
