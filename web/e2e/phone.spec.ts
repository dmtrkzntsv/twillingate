import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { createShare } from './png'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'

// The narrowest common phone. Nothing on any page scrolls sideways at this
// width: not the page, and not a section inside it. The one exception is a
// widget's own content (a wide data table), which scrolls inside its card.
const PHONE = { width: 360, height: 740 }

function authHeaders() {
  return { Authorization: `Bearer ${TOKEN}` }
}

/** What scrolls sideways: the page itself, or any element outside a widget card. */
async function sidewaysScroll(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const out: string[] = []
    const root = document.documentElement
    if (root.scrollWidth > root.clientWidth) out.push(`page: ${root.scrollWidth}px wide in ${root.clientWidth}px`)
    for (const el of Array.from(document.querySelectorAll<HTMLElement>('body *'))) {
      if (el.closest('[data-slot="widget-card"]')) continue
      const x = getComputedStyle(el).overflowX
      if ((x === 'auto' || x === 'scroll') && el.scrollWidth > el.clientWidth + 1) {
        const name = el.getAttribute('aria-label') ?? el.closest('[aria-label]')?.getAttribute('aria-label') ?? ''
        out.push(`${el.tagName.toLowerCase()} in "${name}": ${el.scrollWidth}px wide in ${el.clientWidth}px`)
      }
    }
    return out
  })
}

async function check(page: Page, path: string): Promise<void> {
  await page.goto(path)
  await page.waitForLoadState('networkidle')
  await expect(page.locator('main, [data-slot="sidebar-inset"]').first()).toBeVisible()
  expect(await sidewaysScroll(page), path).toEqual([])
}

async function dashboardIds(request: APIRequestContext): Promise<number[]> {
  const res = await request.get('/api/dashboards', { headers: authHeaders() })
  expect(res.ok(), await res.text()).toBeTruthy()
  const body = (await res.json()) as { dashboards: { dashboard_id: number }[] }
  return body.dashboards.map((d) => d.dashboard_id)
}

/**
 * Whether the element whose own text matches is cut off: wider than its own
 * box, past the viewport's edge, or outside an ancestor that clips (overflow
 * other than visible). Returns what is wrong, empty when it shows whole.
 */
async function clipped(page: Page, text: RegExp): Promise<string[]> {
  return page.evaluate(
    ({ source, flags }) => {
      const re = new RegExp(source, flags)
      const el = Array.from(document.querySelectorAll<HTMLElement>('body *')).find(
        (e) => e.children.length === 0 && re.test(e.textContent ?? '')
      )
      if (!el) return ['not on the page']
      const out: string[] = []
      const r = el.getBoundingClientRect()
      if (el.scrollWidth > el.clientWidth + 1) out.push(`${el.scrollWidth}px of text in ${el.clientWidth}px`)
      if (r.left < 0 || r.right > document.documentElement.clientWidth + 0.5) out.push(`box ${r.left}..${r.right} outside the viewport`)
      for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
        const s = getComputedStyle(a)
        if (s.overflowX === 'visible' && s.overflowY === 'visible') continue
        const b = a.getBoundingClientRect()
        if (r.left < b.left - 0.5 || r.right > b.right + 0.5 || r.bottom > b.bottom + 0.5) out.push(`clipped by <${a.tagName.toLowerCase()}> ${b.left}..${b.right}`)
      }
      return out
    },
    { source: text.source, flags: text.flags }
  )
}

/** A week ending today, as the YYYY-MM-DD range a share is made over. */
function lastWeek(): { from: string; to: string } {
  const day = (d: Date) => d.toISOString().slice(0, 10)
  const now = new Date()
  return { from: day(new Date(now.getTime() - 6 * 86_400_000)), to: day(now) }
}

test.use({ viewport: PHONE })

test('no page scrolls sideways on a phone', async ({ page, request }) => {
  test.setTimeout(120_000)
  // A long name and origins that do not fit a phone's width on one line,
  // as real projects have: they wrap or truncate, never widen the page.
  const created = await request.post('/api/projects', {
    headers: authHeaders(),
    data: {
      name: `phone-${Date.now()} - longer than a phone is wide on one line`,
      allowed_origins: ['https://www.a-long-subdomain.example.com', 'https://billing-test.another-long-subdomain.example.com'],
      attributes: ['months_since_signup_with_a_long_key'],
    },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const { project_id: id } = (await created.json()) as { project_id: number }

  // Two shares of one widget with a 120-character title, over the long-named
  // project: the Shares page and, once one is archived, the Archive page show them.
  const dashboard = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: {
      title: 'Phone shares',
      range: '7d',
      widgets: [{ component: 'stat', title: 'short', source: { type: 'sql', content: 'SELECT 42 AS value' }, width: 6, height: 4 }],
    },
  })
  expect(dashboard.ok(), await dashboard.text()).toBeTruthy()
  const { dashboard_id: dashboardId } = (await dashboard.json()) as { dashboard_id: number }
  const dashboardBody = await request.get(`/api/dashboards/${dashboardId}`, { headers: authHeaders() })
  const widgetId = ((await dashboardBody.json()) as { widgets: { widget_id: number }[] }).widgets[0].widget_id
  const title = `${Date.now()} ${'a long widget title that goes on and on '.repeat(4)}`.slice(0, 120).replace(/ $/, 'x')
  const renamed = await request.patch(`/api/widgets/${widgetId}`, { headers: authHeaders(), data: { title } })
  expect(renamed.ok(), await renamed.text()).toBeTruthy()
  const shares = [
    await createShare(request, { widgetId, projectId: id, ...lastWeek() }),
    await createShare(request, { widgetId, projectId: id, ...lastWeek() }),
  ]

  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  expect(await sidewaysScroll(page), 'login').toEqual([])
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)

  for (const path of ['/app/projects', '/app/projects/1', `/app/projects/${id}`, '/app/archive', '/app/shares', '/app/gallery/components', '/app/gallery/dashboards']) {
    await check(page, path)
  }
  for (const d of await dashboardIds(request)) await check(page, `/app/dashboards/${d}`)

  // The Shares table folds below xl: beside the 256px sidebar it fits at 1024 and 1280 too.
  for (const width of [1024, 1280]) {
    await page.setViewportSize({ width, height: 800 })
    await check(page, '/app/shares')
  }
  await page.setViewportSize(PHONE)

  // The public share page, in both colour schemes.
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    await page.goto(shares[0].url)
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
    await page.waitForLoadState('networkidle')
    expect(await sidewaysScroll(page), `share page (${colorScheme})`).toEqual([])
  }
  await page.emulateMedia({ colorScheme: null })

  // An archived share: its "archived · deleted on <date>" line shows whole, and nothing scrolls.
  const archivedShare = await request.post(`/api/widget-shares/${shares[1].id}/archive`, { headers: authHeaders() })
  expect(archivedShare.ok(), await archivedShare.text()).toBeTruthy()
  await check(page, '/app/archive')
  await expect(page.getByText(/^archived · deleted on /).first()).toBeVisible()
  expect(await clipped(page, /^archived · deleted on /), 'archived share status').toEqual([])

  // The archived projects' grid, opened.
  const archived = await request.post(`/api/projects/${id}/archive`, { headers: authHeaders() })
  expect(archived.ok(), await archived.text()).toBeTruthy()
  await check(page, '/app/projects')
  await page.getByRole('button', { name: /^Archived/ }).click()
  await expect(page.getByRole('article', { name: /^phone-/ })).toBeVisible()
  expect(await sidewaysScroll(page), 'archived projects').toEqual([])
})
