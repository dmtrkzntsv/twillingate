import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
// Copied from app.spec.ts rather than imported across spec files.
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'

function authHeaders() {
  return { Authorization: `Bearer ${TOKEN}` }
}

/** Logs in through the real password page and waits for the app to land on a dashboard. */
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

/**
 * The "E2E …" links under the sidebar's "Dashboards" section, in order. Only
 * this file's own: app.spec.ts runs first on the same server and leaves
 * dashboards of its own there.
 */
async function yoursOrder(page: Page): Promise<string[]> {
  const group = page.locator('[data-sidebar="group"]').filter({ has: page.getByText('Dashboards', { exact: true }) })
  return (await group.getByRole('link').allTextContents()).filter((t) => t.startsWith('E2E '))
}

// Runs every test in this file one at a time: several tests act on the
// same system group (1, "Reports"/"Views"), so running them concurrently
// would race each other's hide/show. The isolation that matters
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

  // Always put group 1 (Reports/Views) back in the sidebar, whatever this
  // test did to it: a no-op when it is already there.
  const ids = await dashboardIds(request)
  const viewsId = ids.get('Views')
  if (viewsId !== undefined) {
    await request.patch(`/api/dashboards/${viewsId}`, { headers: authHeaders(), data: { sidebar: true } })
  }
})

test('duplicates Views from the sidebar without hiding it, then hides it and shows it again from the gallery', async ({ page, request }) => {
  const before = await dashboardIds(request)
  const viewsId = before.get('Views')
  expect(viewsId, 'no dashboard titled Views').toBeDefined()

  await login(page)

  await page.getByRole('link', { name: 'Reports', exact: true }).hover()
  await page.getByRole('button', { name: 'Reports actions' }).click()
  await page.getByRole('menuitem', { name: /^Duplicate/ }).click()

  // Already on /app/dashboards/<viewsId>, so a bare dashboards/\d+ pattern
  // would match the URL we start from; wait for it to name a *different* id.
  await page.waitForURL((url) => /\/app\/dashboards\/\d+/.test(url.pathname) && !url.pathname.endsWith(`/${viewsId}`))
  await page.waitForLoadState('networkidle')
  const copyId = Number(page.url().match(/dashboards\/(\d+)/)?.[1])
  expect(copyId).not.toBe(viewsId)
  toArchive.push({ id: copyId, wholeGroup: true })

  // Duplicating is only a copy: Reports (the group's name) is still in the sidebar alongside it.
  await expect(page.getByRole('tab')).toHaveCount(7)
  await expect(page.getByRole('link', { name: 'Reports', exact: true })).toHaveCount(1)
  // By id: app.spec.ts runs first on the same server and leaves its own
  // "Reports (copy)" behind, so the name alone is not this test's copy.
  await expect(
    page.getByRole('link', { name: 'Reports (copy)', exact: true }).and(page.locator(`[href="/app/dashboards/${copyId}"]`))
  ).toHaveCount(1)

  // Replacing it is a second, explicit step: hide the original.
  await page.getByRole('link', { name: 'Reports', exact: true }).hover()
  await page.getByRole('button', { name: 'Reports actions' }).click()
  await page.getByRole('menuitem', { name: 'Hide' }).click()

  await expect(page.getByRole('link', { name: 'Reports', exact: true })).toHaveCount(0)

  // A hidden system group is not on the Archive page: it comes back from the gallery.
  await page.goto('/app/archive')
  await page.waitForLoadState('networkidle')
  await expect(page.getByRole('main').getByRole('link', { name: 'Views', exact: true })).toHaveCount(0)

  await page.goto('/app/gallery/dashboards')
  await page.waitForLoadState('networkidle')
  const viewsGroup = page.getByRole('main').getByRole('listitem', { name: 'Reports', exact: true })
  await expect(viewsGroup.getByText('7 tabs · hidden')).toBeVisible()
  await viewsGroup.getByRole('button', { name: 'Reports actions', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Show in sidebar' }).click()

  // The gallery card links to its Views tab too; count the sidebar's alone.
  await expect(page.locator('[data-sidebar="sidebar"]').getByRole('link', { name: 'Reports', exact: true })).toHaveCount(1)
  await expect(viewsGroup.getByText('7 tabs', { exact: true })).toBeVisible()
})

test('archives a user tab and undoes it', async ({ page, request }) => {
  const one = await createDashboard(request, 'One')
  toArchive.push({ id: one.id })
  const two = await createDashboard(request, 'Two', one.groupId)
  toArchive.push({ id: two.id })

  await login(page)
  await page.goto(`/app/dashboards/${two.id}`)
  await page.waitForLoadState('networkidle')

  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Archive tab' }).click()

  await expect(page).toHaveURL(new RegExp(`/dashboards/${one.id}$`))
  await expect(page.getByText("Archived 'Two'")).toBeVisible()

  await page.getByRole('button', { name: 'Undo' }).click()
  await expect(page.getByRole('tab', { name: 'Two', exact: true })).toBeVisible()
})

test('duplicates a user tab into its dashboard, or copies it to a new one', async ({ page, request }) => {
  const c1 = await createDashboard(request, 'C1')
  toArchive.push({ id: c1.id, wholeGroup: true })
  const c2 = await createDashboard(request, 'C2', c1.groupId)
  toArchive.push({ id: c2.id })

  await login(page)
  await page.goto(`/app/dashboards/${c2.id}`)
  await page.waitForLoadState('networkidle')

  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Duplicate tab' }).click()
  await page.waitForURL((url) => !url.pathname.endsWith(`/${c2.id}`))
  await expect(page.getByRole('tab')).toHaveText(['C1', 'C2', 'C2 (copy)'])

  await page.getByRole('tab', { name: 'C2', exact: true }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'C2', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Copy to new dashboard' }).click()
  await page.waitForURL((url) => !url.pathname.endsWith(`/${c2.id}`))
  await page.waitForLoadState('networkidle')
  const ownId = Number(page.url().match(/dashboards\/(\d+)/)?.[1])
  toArchive.push({ id: ownId })

  await expect(page.getByRole('tab')).toHaveText(['C2 (copy)'])
  // The source dashboard keeps its three tabs.
  await page.goto(`/app/dashboards/${c1.id}`)
  await expect(page.getByRole('tab')).toHaveText(['C1', 'C2', 'C2 (copy)'])
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

  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Move to' }).click()
  await page.getByRole('menuitem', { name: 'E2E B', exact: true }).click()

  const tablist = page.getByRole('tablist')
  await expect(tablist.getByRole('tab', { name: 'E2E B', exact: true })).toBeVisible()
  await expect(tablist.getByRole('tab', { name: 'A2', exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Move to' }).click()
  await page.getByRole('menuitem', { name: 'Own dashboard' }).click()

  await expect(page.getByRole('tab')).toHaveText(['A2'])
  await expect(page.getByRole('link', { name: 'A2', exact: true })).toBeVisible()
})

test('renames a group from its sidebar menu without touching the dashboard title', async ({ page, request }) => {
  const d = await createDashboard(request, 'E2E Rename')
  toArchive.push({ id: d.id })

  await login(page)
  await page.goto(`/app/dashboards/${d.id}`)
  await page.waitForLoadState('networkidle')

  await page.getByRole('link', { name: 'E2E Rename', exact: true }).hover()
  await page.getByRole('button', { name: 'E2E Rename actions' }).click()
  await page.getByRole('menuitem', { name: 'Rename' }).click()

  const field = page.getByRole('textbox', { name: 'Group name' })
  const save = page.getByRole('button', { name: 'Save group name' })
  await expect(field).toBeFocused()
  await field.fill('a')
  await expect(save).toBeDisabled()
  await expect(field).toHaveAttribute('aria-invalid', 'true')

  await field.fill('Team metrics')
  await field.press('Enter')

  await expect(field).toHaveCount(0)
  const sidebar = page.locator('[data-sidebar="sidebar"]')
  await expect(sidebar.getByRole('link', { name: 'Team metrics', exact: true })).toBeVisible()
  await expect(sidebar.getByRole('link', { name: 'E2E Rename', exact: true })).toHaveCount(0)
  // The group's name is the sidebar's; the page keeps the dashboard's own title.
  await expect(page.getByRole('heading', { level: 1, name: 'E2E Rename', exact: true })).toBeVisible()
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

test('copies a system group and one of its tabs from the gallery while it is hidden', async ({ page, request }) => {
  // Logging in first, while Views is still live, so the post-login
  // redirect ("/") has a live dashboard to land on; it is hidden only
  // after we are already signed in.
  await login(page)

  const ids = await dashboardIds(request)
  const viewsId = ids.get('Views')
  expect(viewsId, 'no dashboard titled Views').toBeDefined()
  const hidden = await request.patch(`/api/dashboards/${viewsId}`, {
    headers: authHeaders(),
    data: { sidebar: false },
  })
  expect(hidden.ok(), await hidden.text()).toBeTruthy()

  await page.goto('/app/gallery/dashboards')
  await page.waitForLoadState('networkidle')

  const main = page.getByRole('main')
  const viewsGroup = main.getByRole('listitem', { name: 'Reports', exact: true })
  // The Dashboards gallery shows a hidden group as hidden, never "archived" (D17).
  await expect(viewsGroup.getByText(/archived/i)).toHaveCount(0)
  await viewsGroup.getByRole('button', { name: 'Reports actions', exact: true }).click()
  await page.getByRole('menuitem', { name: 'Duplicate dashboard' }).click()

  await page.waitForURL(/\/app\/dashboards\/\d+/)
  await page.waitForLoadState('networkidle')
  const groupCopyId = Number(page.url().match(/dashboards\/(\d+)/)?.[1])
  toArchive.push({ id: groupCopyId, wholeGroup: true })
  await expect(page.getByRole('heading', { level: 1, name: 'Views (copy)', exact: true })).toBeVisible()
  await expect(page.getByRole('tab')).toHaveCount(7)

  // One tab of the hidden template, from that tab's row in the gallery.
  await page.goto('/app/gallery/dashboards')
  await viewsGroup.getByRole('button', { name: 'Users tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Copy to new dashboard' }).click()

  await page.waitForURL((url) => /\/app\/dashboards\/\d+/.test(url.pathname) && !url.pathname.endsWith(`/${groupCopyId}`))
  await page.waitForLoadState('networkidle')
  const copyId = Number(page.url().match(/dashboards\/(\d+)/)?.[1])
  toArchive.push({ id: copyId })

  await expect(page.getByRole('heading', { level: 1, name: 'Users (copy)', exact: true })).toBeVisible()
  await expect(page.getByRole('tab')).toHaveText(['Users (copy)'])
})
