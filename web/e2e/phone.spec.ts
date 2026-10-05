import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

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

test.use({ viewport: PHONE })

test('no page scrolls sideways on a phone', async ({ page, request }) => {
  test.setTimeout(120_000)
  // A long name and origins that do not fit a phone's width on one line,
  // as real projects have: they wrap or truncate, never widen the page.
  const created = await request.post('/api/projects', {
    headers: authHeaders(),
    data: {
      name: `phone-${Date.now()} - a project name longer than a phone is wide`,
      allowed_origins: ['https://www.a-long-subdomain.example.com', 'https://billing-test.another-long-name.example.com'],
      attributes: ['months_since_signup_with_a_long_name'],
    },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const { project_id: id } = (await created.json()) as { project_id: number }

  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  expect(await sidewaysScroll(page), 'login').toEqual([])
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)

  for (const path of ['/app/projects', '/app/projects/1', `/app/projects/${id}`, '/app/archive', '/app/gallery/components', '/app/gallery/dashboards']) {
    await check(page, path)
  }
  for (const d of await dashboardIds(request)) await check(page, `/app/dashboards/${d}`)

  // The archived projects' grid, opened.
  const archived = await request.post(`/api/projects/${id}/archive`, { headers: authHeaders() })
  expect(archived.ok(), await archived.text()).toBeTruthy()
  await check(page, '/app/projects')
  await page.getByRole('button', { name: /^Archived/ }).click()
  await expect(page.getByRole('article', { name: /^phone-/ })).toBeVisible()
  expect(await sidewaysScroll(page), 'archived projects').toEqual([])
})
