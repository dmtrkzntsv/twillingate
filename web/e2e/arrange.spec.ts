import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'

// Matches web/e2e/serve.sh's API_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
// Copied from app.spec.ts rather than imported across spec files.
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'

function authHeaders() {
  return { Authorization: `Bearer ${TOKEN}` }
}

/** Logs in through the real password page and waits for the app to land on a dashboard. */
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

/** Creates a user dashboard over REST, optionally as a tab of an existing group. */
async function createDashboard(
  request: APIRequestContext,
  title: string,
  groupId?: number
): Promise<{ id: number; groupId: number }> {
  const res = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: { title, range: '7d', ...(groupId ? { group_id: groupId } : {}) },
  })
  expect(res.ok(), await res.text()).toBeTruthy()
  const body = (await res.json()) as { dashboard_id: number; group_id: number }
  return { id: body.dashboard_id, groupId: body.group_id }
}

/**
 * Drags `source` to `target`'s position with raw mouse events (dnd-kit's
 * PointerSensor reads pointer/mouse events, not the HTML5 drag-and-drop
 * API, so a plain `locator.dragTo` without intermediate steps is not
 * reliable here). The first move clears the 6px activation distance
 * before heading toward the target.
 */
async function dragAbove(page: Page, source: Locator, target: Locator): Promise<void> {
  const from = await source.boundingBox()
  const to = await target.boundingBox()
  if (!from || !to) throw new Error('drag source or target has no bounding box')
  const sx = from.x + from.width / 2
  const sy = from.y + from.height / 2
  const tx = to.x + to.width / 2
  const ty = to.y + 2
  await page.mouse.move(sx, sy)
  await page.mouse.down()
  await page.mouse.move(sx, sy - 15, { steps: 5 })
  await page.mouse.move(tx, ty, { steps: 10 })
  await page.mouse.up()
}

/** The accessible names of the links under the sidebar's "Yours" section, in order. */
async function yoursOrder(page: Page): Promise<string[]> {
  const group = page.locator('[data-sidebar="group"]').filter({ has: page.getByText('Yours', { exact: true }) })
  return group.getByRole('link').allTextContents()
}

// Runs every test in this file one at a time: several tests act on the
// same system group (1, "Reports"/"Views"), so running them concurrently
// would race each other's archive/restore. The isolation that matters
// against *other* spec files (app.spec.ts assumes Reports stays live) is
// handled in playwright.config.ts's `arrange` project, which only starts
// once the main project has finished.
test.describe.configure({ mode: 'serial' })

// User dashboards this file created, archived in afterEach. Most are
// pushed one id at a time (archiving by id alone cleans it up regardless
// of its current group, since by the end of a test they may be split
// across several groups); a whole-group duplicate of a system dashboard
// is pushed once with `wholeGroup: true` so its other copied tabs (which
// never get their own id back from the client) are archived too.
// Archiving an already-archived dashboard is a harmless no-op.
let toArchive: { id: number; wholeGroup?: boolean }[] = []

test.beforeEach(() => {
  toArchive = []
})

test.afterEach(async ({ request }) => {
  for (const { id, wholeGroup } of toArchive) {
    await request.post(`/api/dashboards/${id}/archive`, { headers: authHeaders(), data: wholeGroup ? { whole_group: true } : {} })
  }
  toArchive = []

  // Always restore group 1 (Reports/Views), whatever this test did to it:
  // a no-op when it is already live.
  const ids = await dashboardIds(request)
  const viewsId = ids.get('Views')
  if (viewsId !== undefined) {
    await request.post(`/api/dashboards/${viewsId}/restore`, { headers: authHeaders(), data: { whole_group: true } })
  }
})

test('duplicates Views from the sidebar without archiving it, then archives and restores it', async ({ page, request }) => {
  const before = await dashboardIds(request)
  const viewsId = before.get('Views')
  expect(viewsId, 'no dashboard titled Views').toBeDefined()

  await login(page)

  await page.getByRole('link', { name: 'Views', exact: true }).hover()
  await page.getByRole('button', { name: 'Views actions' }).click()
  await page.getByRole('menuitem', { name: /^Duplicate/ }).click()

  // Already on /app/dashboards/<viewsId>, so a bare dashboards/\d+ pattern
  // would match the URL we start from; wait for it to name a *different* id.
  await page.waitForURL((url) => /\/app\/dashboards\/\d+/.test(url.pathname) && !url.pathname.endsWith(`/${viewsId}`))
  await page.waitForLoadState('networkidle')
  const copyId = Number(page.url().match(/dashboards\/(\d+)/)?.[1])
  expect(copyId).not.toBe(viewsId)
  toArchive.push({ id: copyId, wholeGroup: true })

  // Duplicating is only a copy: Views is still in the sidebar alongside it.
  await expect(page.getByRole('tab')).toHaveCount(5)
  await expect(page.getByRole('link', { name: 'Views', exact: true })).toHaveCount(1)
  await expect(page.getByRole('link', { name: 'Views (copy)', exact: true })).toHaveCount(1)

  // Replacing it is a second, explicit step: archive the original.
  await page.getByRole('link', { name: 'Views', exact: true }).hover()
  await page.getByRole('button', { name: 'Views actions' }).click()
  await page.getByRole('menuitem', { name: 'Archive' }).click()

  await expect(page.getByRole('link', { name: 'Views', exact: true })).toHaveCount(0)

  await page.goto('/app/gallery/dashboards')
  await page.waitForLoadState('networkidle')
  const viewsCard = page.locator('li').filter({ has: page.getByRole('heading', { level: 3, name: 'Views', exact: true }) })
  await viewsCard.getByRole('button', { name: 'Restore' }).click()

  await expect(page.getByRole('link', { name: 'Views', exact: true })).toHaveCount(1)
})

test('archives a user tab and undoes it', async ({ page, request }) => {
  const one = await createDashboard(request, 'One')
  toArchive.push({ id: one.id })
  const two = await createDashboard(request, 'Two', one.groupId)
  toArchive.push({ id: two.id })

  await login(page)
  await page.goto(`/app/dashboards/${two.id}`)
  await page.waitForLoadState('networkidle')

  await page.getByRole('button', { name: 'Dashboard actions' }).click()
  await page.getByRole('menuitem', { name: 'Archive tab' }).click()

  await expect(page).toHaveURL(new RegExp(`/dashboards/${one.id}$`))
  await expect(page.getByText("Archived 'Two'")).toBeVisible()

  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page.getByRole('tab', { name: 'Two', exact: true })).toBeVisible()
})

test('moves a tab between groups', async ({ page, request }) => {
  const a1 = await createDashboard(request, 'A1')
  toArchive.push({ id: a1.id })
  const a2 = await createDashboard(request, 'A2', a1.groupId)
  toArchive.push({ id: a2.id })
  const b = await createDashboard(request, 'E2E B')
  toArchive.push({ id: b.id })

  await login(page)
  await page.goto(`/app/dashboards/${a2.id}`)
  await page.waitForLoadState('networkidle')

  await page.getByRole('button', { name: 'Dashboard actions' }).click()
  await page.getByRole('menuitem', { name: 'Move to' }).click()
  await page.getByRole('menuitem', { name: 'E2E B', exact: true }).click()

  const tablist = page.getByRole('tablist')
  await expect(tablist.getByRole('tab', { name: 'E2E B', exact: true })).toBeVisible()
  await expect(tablist.getByRole('tab', { name: 'A2', exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Dashboard actions' }).click()
  await page.getByRole('menuitem', { name: 'Move to' }).click()
  await page.getByRole('menuitem', { name: 'Own dashboard' }).click()

  await expect(page.getByRole('tablist')).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'A2', exact: true })).toBeVisible()
})

test('drag reorder persists across a reload', async ({ page, request }) => {
  const x = await createDashboard(request, 'E2E X')
  toArchive.push({ id: x.id })
  const y = await createDashboard(request, 'E2E Y')
  toArchive.push({ id: y.id })

  await login(page)
  await page.goto(`/app/dashboards/${x.id}`)
  await page.waitForLoadState('networkidle')

  await expect.poll(() => yoursOrder(page)).toEqual(['E2E X', 'E2E Y'])

  const yLink = page.getByRole('link', { name: 'E2E Y', exact: true })
  const xLink = page.getByRole('link', { name: 'E2E X', exact: true })
  await dragAbove(page, yLink, xLink)

  await expect.poll(() => yoursOrder(page)).toEqual(['E2E Y', 'E2E X'])

  await page.reload()
  await page.waitForLoadState('networkidle')
  await expect.poll(() => yoursOrder(page)).toEqual(['E2E Y', 'E2E X'])
})

test('copies a system tab from the gallery while Reports is archived', async ({ page, request }) => {
  // Logging in first, while Views is still live, so the post-login
  // redirect ("/") has a live dashboard to land on; it is archived only
  // after we are already signed in.
  await login(page)

  const ids = await dashboardIds(request)
  const viewsId = ids.get('Views')
  expect(viewsId, 'no dashboard titled Views').toBeDefined()
  const archived = await request.post(`/api/dashboards/${viewsId}/archive`, {
    headers: authHeaders(),
    data: { whole_group: true },
  })
  expect(archived.ok(), await archived.text()).toBeTruthy()

  await page.goto('/app/gallery/dashboards')
  await page.waitForLoadState('networkidle')

  const viewsCard = page.locator('li').filter({ has: page.getByRole('heading', { level: 3, name: 'Views', exact: true }) })
  const usersRow = viewsCard.locator('li').filter({ hasText: 'Users' })
  await usersRow.getByRole('button', { name: 'Copy as a dashboard' }).click()

  await page.waitForURL(/\/app\/dashboards\/\d+/)
  await page.waitForLoadState('networkidle')
  const copyId = Number(page.url().match(/dashboards\/(\d+)/)?.[1])
  toArchive.push({ id: copyId })

  await expect(page.getByRole('heading', { level: 1, name: 'Users (copy)', exact: true })).toBeVisible()
  await expect(page.getByRole('tab')).toHaveCount(0)
})
