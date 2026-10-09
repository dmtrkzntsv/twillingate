import { expect as base, test, type APIRequestContext, type Page } from '@playwright/test'
import { createProject } from './forms'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'
const BUILT_INS = ['Views', 'Product', 'Users', 'Groups', 'Retention', 'Web Vitals', 'Measures']
// A built-in tab removed and added back goes last, like a tab a release adds.
const READDED = [...BUILT_INS.filter((name) => name !== 'Product'), 'Product']

// Each tab change is a write, which on a loaded host can wait seconds for
// SQLite behind the other spec files' writes and the server's startup pass.
const expect = base.configure({ timeout: 20_000 })

/** Archived once its test is done, so a card of its own never shifts the projects grid under another spec. */
async function archiveProject(request: APIRequestContext, id: number): Promise<void> {
  await request.post(`/api/projects/${id}/archive`, { headers: authHeaders() })
}

function authHeaders() {
  return { Authorization: `Bearer ${TOKEN}` }
}

async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)
}

test('a project opens on its first dashboard tab; tabs are removed and re-added from the project page', async ({
  page,
  request,
}) => {
  test.setTimeout(120_000)
  const headers = authHeaders()
  // A dashboard of our own to add: a copy of Views, made over the API and
  // renamed, so the dashboards other spec files leave behind never share its title.
  const copy = await request.post('/api/dashboards/1/duplicate', { headers, data: {} })
  expect(copy.ok(), await copy.text()).toBeTruthy()
  const { dashboard_id: mineId } = (await copy.json()) as { dashboard_id: number }
  const title = `E2E project tab ${Date.now()}`
  const renamed = await request.patch(`/api/dashboards/${mineId}`, { headers, data: { title } })
  expect(renamed.ok(), await renamed.text()).toBeTruthy()
  // A second one, to reorder against the first.
  const copy2 = await request.post('/api/dashboards/1/duplicate', { headers, data: {} })
  expect(copy2.ok(), await copy2.text()).toBeTruthy()
  const { dashboard_id: secondId } = (await copy2.json()) as { dashboard_id: number }
  const second = `${title} second`
  const renamed2 = await request.patch(`/api/dashboards/${secondId}`, { headers, data: { title: second } })
  expect(renamed2.ok(), await renamed2.text()).toBeTruthy()

  await login(page)
  await page.getByRole('article', { name: 'dev' }).getByRole('link', { name: 'dev' }).click()
  await expect(page).toHaveURL(/\/app\/projects\/\d+\/dashboards\/\d+$/)
  const projectURL = new URL(page.url()).pathname.replace(/\/dashboards\/\d+$/, '')
  const tabs = page.getByRole('tablist', { name: 'Tabs' })
  await expect(tabs.getByRole('tab')).toHaveText([...BUILT_INS])

  // A built-in tab, removed from its own menu, comes back from "+", last.
  await tabs.getByRole('tab', { name: 'Product', exact: true }).click()
  await expect(page).toHaveURL(/\/projects\/\d+\/dashboards\/\d+$/)
  await expect(page.getByRole('heading', { level: 1, name: 'Product', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Remove from this project' }).click()
  // The toast first: until the menu has closed, the tab row is hidden from
  // the accessibility tree and would count no tabs at all.
  await expect(page.getByText("Removed 'Product'")).toBeVisible()
  await expect(tabs.getByRole('tab', { name: 'Product', exact: true })).toHaveCount(0)
  // It lands on the tab before it.
  await expect(tabs.getByRole('tab', { name: 'Views', exact: true })).toHaveAttribute('data-state', 'active')

  await page.getByRole('button', { name: 'Add tab' }).click()
  // Whole-group copies other spec files make have a "Product" of their own.
  await page.getByRole('dialog').getByRole('group', { name: 'Built-in' }).getByRole('button', { name: 'Product', exact: true }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(tabs.getByRole('tab')).toHaveText([...READDED])
  await expect(tabs.getByRole('tab', { name: 'Product', exact: true })).toHaveAttribute('data-state', 'active')

  // A dashboard of our own joins after the built-ins.
  await page.getByRole('button', { name: 'Add tab' }).click()
  await page.getByRole('dialog').getByRole('group', { name: 'Your dashboards' }).getByRole('button', { name: title, exact: true }).click()
  await expect(tabs.getByRole('tab')).toHaveText([...READDED, title])
  await expect(page.getByRole('heading', { level: 1, name: title, exact: true })).toBeVisible()

  // Your own tabs reorder; a phone does it from the tab's menu (a wide
  // screen drags), and the new order is the project's, after a reload too.
  await page.getByRole('button', { name: 'Add tab' }).click()
  await page.getByRole('dialog').getByRole('group', { name: 'Your dashboards' }).getByRole('button', { name: second, exact: true }).click()
  await expect(tabs.getByRole('tab')).toHaveText([...READDED, title, second])
  await expect(page.getByRole('heading', { level: 1, name: second, exact: true })).toBeVisible()
  await page.setViewportSize({ width: 390, height: 800 })
  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Move left' }).click()
  await page.setViewportSize({ width: 1280, height: 800 })
  await expect(tabs.getByRole('tab')).toHaveText([...READDED, second, title])
  await page.reload()
  await expect(tabs.getByRole('tab')).toHaveText([...READDED, second, title])
  // Back to one tab of our own for the rest of the flow.
  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Remove from this project' }).click()
  await expect(page.getByText(`Removed '${second}'`)).toBeVisible()
  await expect(tabs.getByRole('tab')).toHaveText([...READDED, title])

  // Our own dashboard's last project tab can go: it stays in the sidebar,
  // and "+" brings the tab back. Projects are chosen one at a time, here.
  await tabs.getByRole('tab', { name: title, exact: true }).click()
  await expect(page.getByRole('heading', { level: 1, name: title, exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Remove from this project' }).click()
  await expect(page.getByText(`Removed '${title}'`)).toBeVisible()
  await expect(tabs.getByRole('tab')).toHaveText([...READDED])
  await expect(page.locator('[data-sidebar="sidebar"]').getByRole('link', { name: title, exact: true })).toBeVisible()
  await page.reload()
  await expect(tabs.getByRole('tab')).toHaveText([...READDED])
  await page.getByRole('button', { name: 'Add tab' }).click()
  await page.getByRole('dialog').getByRole('group', { name: 'Your dashboards' }).getByRole('button', { name: title, exact: true }).click()
  await expect(tabs.getByRole('tab')).toHaveText([...READDED, title])

  // Settings is a button beside the tabs, not a tab; the old /setup address still reaches it.
  await expect(tabs.getByRole('tab', { name: 'Settings' })).toHaveCount(0)
  await page.getByRole('link', { name: 'Settings', exact: true }).click()
  await expect(page).toHaveURL(/\/settings$/)
  await expect(page.getByRole('link', { name: 'Settings', exact: true })).toHaveAttribute('aria-current', 'page')
  await expect(page.getByRole('region', { name: 'Allowed origins' })).toBeVisible()
  await page.goto(`${projectURL}/setup`)
  await expect(page).toHaveURL(/\/settings$/)
  await expect(tabs.getByRole('tab')).toHaveText([...READDED, title])

  // Product goes back after Views, where the other specs expect to find it.
  const projectId = Number(projectURL.split('/').pop())
  const listed = await request.get(`/api/projects/${projectId}/tabs`, { headers })
  expect(listed.ok(), await listed.text()).toBeTruthy()
  const listedTabs = ((await listed.json()) as { tabs: { dashboard_id: number; title: string }[] }).tabs
  const viewsTab = listedTabs.find((t) => t.title === 'Views')
  const productTab = listedTabs.find((t) => t.title === 'Product')
  expect(viewsTab && productTab, 'Views and Product tabs').toBeTruthy()
  const restored = await request.post(`/api/projects/${projectId}/tabs/${productTab!.dashboard_id}/move`, {
    headers,
    data: { after: viewsTab!.dashboard_id },
  })
  expect(restored.ok(), await restored.text()).toBeTruthy()

  // Archiving our own dashboard takes its tab away. The second goes too:
  // arrange.spec.ts expects no dashboards of ours left in the sidebar.
  const archived = await request.post(`/api/dashboards/${mineId}/archive`, { headers, data: {} })
  expect(archived.ok(), await archived.text()).toBeTruthy()
  const archived2 = await request.post(`/api/dashboards/${secondId}/archive`, { headers, data: {} })
  expect(archived2.ok(), await archived2.text()).toBeTruthy()
  await page.reload()
  await expect(page.getByRole('link', { name: 'Settings', exact: true })).toBeVisible()
  await expect(tabs.getByRole('tab', { name: title, exact: true })).toHaveCount(0)
})

test('a project opens on the last tab used there', async ({ page, request }) => {
  // A project of its own, so the dev project's tab order stays the other tests'.
  const { id } = await createProject(request, `landing-${Date.now()}`, [])
  try {
    await login(page)
    await page.goto(`/app/projects/${id}`)
    await expect(page).toHaveURL(new RegExp(`/projects/${id}/dashboards/\\d+`))
    const tabs = page.getByRole('tab')
    await tabs.nth(1).click()
    await expect(tabs.nth(1)).toHaveAttribute('data-state', 'active')
    const second = page.url()
    // The tab is remembered once its content has mounted; leave only after that.
    const secondId = Number(second.split('/').pop())
    await expect
      .poll(() => page.evaluate((key) => JSON.parse(localStorage.getItem(key) ?? '{}'), 'twillingate.project.last_tab'))
      .toMatchObject({ [String(id)]: secondId })
    await page.getByRole('link', { name: 'Settings', exact: true }).click()
    await expect(page).toHaveURL(new RegExp(`/projects/${id}/settings`))
    await page.goto(`/app/projects/${id}`)
    await expect(page).toHaveURL(second)
  } finally {
    await archiveProject(request, id)
  }
})

test('a built-in tab drags to the end', async ({ page, request }) => {
  test.setTimeout(60_000)
  const { id } = await createProject(request, `drag-${Date.now()}`, [])
  try {
    await login(page)
    await page.goto(`/app/projects/${id}`)
    const tabs = page.getByRole('tablist', { name: 'Tabs' }).getByRole('tab')
    await expect(tabs.first()).toBeVisible()
    const before = await tabs.allTextContents()
    expect(before.length).toBeGreaterThan(2)
    const first = before[0]
    const from = await tabs.first().boundingBox()
    const to = await tabs.last().boundingBox()
    if (!from || !to) throw new Error('a tab has no bounding box')
    // Raw mouse moves: dnd-kit reads pointer events, and the first move
    // must clear its activation distance.
    await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2)
    const moved = page.waitForResponse((r) => /\/tabs\/\d+\/move$/.test(r.url()) && r.request().method() === 'POST')
    await page.mouse.down()
    await page.mouse.move(from.x + from.width / 2 + 15, from.y + from.height / 2, { steps: 5 })
    await page.mouse.move(to.x + to.width - 4, to.y + to.height / 2, { steps: 10 })
    await page.mouse.up()
    expect((await moved).ok()).toBeTruthy()
    await expect(tabs.last()).toHaveText(first)
    await page.reload()
    await expect(tabs.last()).toHaveText(first)
  } finally {
    await archiveProject(request, id)
  }
})
