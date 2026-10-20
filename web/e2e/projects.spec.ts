import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { createProject as createFormsProject, submitJSON } from './forms'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'

// Signs in from the console's address, which opens the projects.
async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)
}

test('creates a project, edits it, manages a key, archives and restores it', async ({ page }) => {
  await login(page)
  await page.goto('/app/projects')
  await expect(page.getByRole('article', { name: 'dev' })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Limits' })).toBeVisible()

  const name = `e2e-${Date.now()}`
  await page.getByRole('button', { name: 'New project' }).click()
  await page.getByLabel('Name', { exact: true }).fill(name)
  await page.getByRole('button', { name: 'Add origin' }).click()
  await page.getByRole('textbox', { name: 'Origin 1' }).fill('https://e2e.example')
  await page.getByRole('button', { name: 'Create' }).click()
  await expect(page.getByText('Project created')).toBeVisible()
  await expect(page.getByText(/^ak_/)).toBeVisible()
  await page.keyboard.press('Escape')

  await page.getByRole('article', { name }).getByRole('link', { name }).click()
  // A project opens on its first dashboard tab; its settings are behind the gear.
  await expect(page).toHaveURL(/\/projects\/\d+\/dashboards\/\d+$/)
  await page.getByRole('link', { name: 'Settings', exact: true }).click()
  await expect(page).toHaveURL(/\/projects\/\d+\/settings$/)
  await expect(page.getByRole('heading', { name })).toBeVisible()

  await page.getByRole('button', { name: 'Rename' }).click()
  await page.getByRole('textbox', { name: 'Project name' }).fill(`${name}-renamed`)
  await page.keyboard.press('Enter')
  await expect(page.getByRole('heading', { level: 1, name: `${name}-renamed` })).toBeVisible()

  const origins = page.getByRole('region', { name: 'Allowed origins' })
  await origins.getByRole('button', { name: 'Add origin' }).click()
  await page.getByLabel('Origin', { exact: true }).fill('https://app.e2e.example')
  await page.getByRole('button', { name: 'Add', exact: true }).click()
  await expect(origins.getByText('https://app.e2e.example', { exact: true })).toBeVisible()
  await origins.getByRole('button', { name: 'Allow everything' }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Allow everything' }).click()
  await expect(origins.getByText('*', { exact: true })).toBeVisible()
  await expect(origins.getByRole('button', { name: 'Allow everything' })).toHaveCount(0)
  await origins.getByRole('button', { name: 'Remove https://e2e.example' }).click()
  await page.getByRole('button', { name: 'Remove origin' }).click()
  await expect(origins.getByText('https://e2e.example', { exact: true })).toHaveCount(0)

  const keys = page.getByRole('region', { name: 'Ingest keys' })
  await keys.getByRole('button', { name: 'Issue key' }).click()
  await page.getByLabel('Label').fill('ios app')
  await page.getByRole('button', { name: 'Issue', exact: true }).click()
  await expect(page.getByText('Key issued')).toBeVisible()
  await page.keyboard.press('Escape')
  await keys.getByRole('button', { name: 'Disable ios app' }).click()
  await page.getByRole('button', { name: 'Disable key' }).click()
  await expect(keys.getByRole('row', { name: /ios app/ }).getByText('disabled')).toBeVisible()

  await page.getByRole('button', { name: 'Archive', exact: true }).click()
  await page.getByRole('button', { name: 'Archive project' }).click()
  // The badge on the page; a toast says "Archived" too.
  await expect(page.getByRole('main').getByText('Archived', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Restore' }).click()
  await expect(page.getByRole('button', { name: 'Archive', exact: true })).toBeVisible()

  // Archived again, it leaves the Projects page for the Archive page, which restores it too.
  await page.getByRole('button', { name: 'Archive', exact: true }).click()
  await page.getByRole('button', { name: 'Archive project' }).click()
  await expect(page.getByRole('main').getByText('Archived', { exact: true })).toBeVisible()
  await page.goto('/app/projects')
  await expect(page.getByRole('article', { name: 'dev' })).toBeVisible()
  await expect(page.getByRole('article', { name })).toHaveCount(0)
  await page.goto('/app/archive')
  const row = page.getByRole('region', { name: 'Projects' }).getByRole('listitem').filter({ hasText: name })
  await row.getByRole('button', { name: 'Restore' }).click()
  await expect(row).toHaveCount(0)
  await page.goto('/app/projects')
  await expect(page.getByRole('article', { name })).toBeVisible()
})

test('shows usage for the seeded project', async ({ page }) => {
  await login(page)
  await page.goto('/app/projects')
  await page.getByRole('article', { name: 'dev' }).getByRole('link', { name: 'dev' }).click()
  await page.getByRole('link', { name: 'Settings', exact: true }).click()
  const usage = page.getByRole('region', { name: 'Usage' })
  await expect(usage.getByText('Events', { exact: true })).toBeVisible()
  await expect(usage.locator('.recharts-bar-rectangle').first()).toBeVisible()
})

test('lists Projects first in the sidebar, with the projects under it, closed state remembered', async ({ page }) => {
  await login(page)
  const sidebar = page.locator('[data-sidebar="sidebar"]')
  await expect(sidebar.getByRole('link', { name: 'Projects' })).toHaveAttribute('href', '/app/projects')
  await expect(sidebar.getByText('Dashboards', { exact: true })).toBeVisible()
  await expect(sidebar.getByRole('list', { name: 'Built-in dashboards' }).getByRole('link', { name: 'Reports', exact: true })).toBeVisible()

  const projects = sidebar.getByRole('list', { name: 'Projects' })
  const dev = projects.getByRole('link', { name: 'dev', exact: true })
  await dev.click()
  await expect(page).toHaveURL(/\/app\/projects\/\d+\/dashboards\/\d+$/)
  await expect(dev).toHaveAttribute('data-active', 'true')

  await sidebar.getByRole('button', { name: 'Show projects' }).click()
  await expect(projects).toHaveCount(0)
  await page.reload()
  await expect(sidebar.getByRole('link', { name: 'Projects' })).toHaveAttribute('data-active', 'true')
  await expect(projects).toHaveCount(0)
})

test('switches a breakdown on for an attribute the seeded project received, then off', async ({ page }) => {
  // Login, the wait below and the cleanup share one test limit, which must
  // outlast the wait's own 90s or the default 30s would cut it short.
  test.setTimeout(120_000)
  await login(page)
  await page.goto('/app/projects')
  await page.getByRole('article', { name: 'dev' }).getByRole('link', { name: 'dev' }).click()
  await page.getByRole('link', { name: 'Settings', exact: true }).click()
  const attributes = page.getByRole('region', { name: 'Attributes' })
  const row = attributes.getByRole('row', { name: /Break down plan\b/ })
  // The pass that counts the seeded days runs after the server starts listening
  // (about 20s on a loaded host), so reload, which refetches the keys, until
  // `plan` is listed. The wait is bounded by the toPass timeout (90s), inside
  // the test's 120s limit set above.
  await expect(async () => {
    try {
      await expect(row).toBeVisible({ timeout: 3_000 })
    } catch (e) {
      await page.reload()
      throw e
    }
  }).toPass({ timeout: 90_000, intervals: [2_000] })
  await expect(row.getByRole('cell', { name: /events · \d+ values?/ })).toBeVisible()
  // plan stays undeclared in the seed; the switch declares it. It turns on
  // once the attributes are fetched again, so wait for it.
  const plan = attributes.getByRole('switch', { name: 'Break down plan', exact: true })
  await expect(plan).not.toBeChecked()
  await plan.click()
  await expect(plan).toBeChecked()
  // Leave the seed as it was for the other tests.
  await plan.click()
  await page.getByRole('button', { name: 'Remove breakdown' }).click()
  await expect(plan).not.toBeChecked()
})

/** Creates a project over REST, archived afterwards so it leaves the grid to the other specs. */
async function createProject(request: APIRequestContext, name: string): Promise<number> {
  const res = await request.post('/api/projects', { headers: { Authorization: `Bearer ${TOKEN}` }, data: { name, skip_key: true } })
  expect(res.ok(), await res.text()).toBeTruthy()
  return ((await res.json()) as { project_id: number }).project_id
}

test('drags a project card to a new place, which a reload keeps', async ({ page, request }) => {
  const stamp = Date.now()
  const names = [`order-a-${stamp}`, `order-b-${stamp}`]
  const ids = [await createProject(request, names[0]), await createProject(request, names[1])]
  try {
    await login(page)
    // Cards whose project has forms grow a line when the activity arrives,
    // which would move the cards under a drag measured before it.
    const activity = page.waitForResponse((r) => r.url().endsWith('/api/project-activity') && r.ok())
    await page.goto('/app/projects')
    await activity
    const mine = async () =>
      (await page.getByRole('article').evaluateAll((els) => els.map((e) => e.getAttribute('aria-label')))).filter((n) =>
        names.includes(n ?? '')
      )
    await expect.poll(mine).toEqual(names)

    // Other spec files add projects, so the two new cards can sit below
    // the fold: bring them into view, or the mouse moves off-screen.
    await page.getByRole('article', { name: names[1] }).scrollIntoViewIfNeeded()

    // Raw mouse moves: dnd-kit reads pointer events, and the first move
    // clears its 6px activation distance.
    const from = (await page.getByRole('article', { name: names[1] }).boundingBox())!
    const to = (await page.getByRole('article', { name: names[0] }).boundingBox())!
    await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2)
    await page.mouse.down()
    await page.mouse.move(from.x + from.width / 2 - 15, from.y + from.height / 2, { steps: 5 })
    await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2, { steps: 10 })
    await page.mouse.up()
    // The write waits behind the other spec files' on a loaded host.
    await expect.poll(mine, { timeout: 15_000 }).toEqual([names[1], names[0]])
    // The drop opened nothing.
    await expect(page).toHaveURL(/\/app\/projects$/)

    await page.reload()
    await expect.poll(mine).toEqual([names[1], names[0]])
  } finally {
    for (const id of ids) await request.post(`/api/projects/${id}/archive`, { headers: { Authorization: `Bearer ${TOKEN}` } })
  }
})

test('drags a project in the sidebar to a new place, which the Projects page and a reload keep', async ({ page, request }) => {
  const stamp = Date.now()
  const names = [`side-a-${stamp}`, `side-b-${stamp}`]
  const ids = [await createProject(request, names[0]), await createProject(request, names[1])]
  try {
    await login(page)
    const list = page.locator('[data-sidebar="sidebar"]').getByRole('list', { name: 'Projects' })
    const mine = async () => (await list.getByRole('link').allTextContents()).filter((n) => names.includes(n))
    await expect.poll(mine).toEqual(names)

    const fromLink = list.getByRole('link', { name: names[1], exact: true })
    await fromLink.scrollIntoViewIfNeeded()
    const from = (await fromLink.boundingBox())!
    const to = (await list.getByRole('link', { name: names[0], exact: true }).boundingBox())!
    await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2)
    await page.mouse.down()
    await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2 - 8, { steps: 5 })
    await page.mouse.move(to.x + to.width / 2, to.y + to.height / 2 - 4, { steps: 10 })
    await page.mouse.up()
    await expect.poll(mine).toEqual([names[1], names[0]])
    // The drop opened nothing.
    await expect(page).toHaveURL(/\/app\/projects$/)

    await page.reload()
    await expect.poll(mine).toEqual([names[1], names[0]])
    const cards = async () =>
      (await page.getByRole('article').evaluateAll((els) => els.map((e) => e.getAttribute('aria-label')))).filter((n) =>
        names.includes(n ?? '')
      )
    await expect.poll(cards).toEqual([names[1], names[0]])
  } finally {
    for (const id of ids) await request.post(`/api/projects/${id}/archive`, { headers: { Authorization: `Bearer ${TOKEN}` } })
  }
})

test("shows a project's forms on its card and a dot in the sidebar until they are read", async ({ page, request }) => {
  const name = `e2e-forms-${Date.now()}`
  const project = await createFormsProject(request, name, [])
  try {
    await submitJSON(request, project.key, 'contact', { email: 'ann@example.com' })
    await login(page)
    const sidebar = page.locator('[data-sidebar="sidebar"]')
    await expect(sidebar.getByRole('link', { name: `${name}, new in Forms`, exact: true })).toBeVisible()

    const line = page.getByRole('article', { name }).getByRole('link', { name: '1 form · 1 draft · 1 new submission', exact: true })
    await line.click()
    await expect(page).toHaveURL(new RegExp(`/projects/${project.id}/forms$`))

    // Reading the form clears the new submission; the draft still waits, so the dot stays.
    const seen = page.waitForResponse((r) => r.url().endsWith('/forms/contact/seen') && r.ok())
    await page.getByRole('list', { name: 'Forms' }).getByRole('link').filter({ hasText: 'contact' }).click()
    await seen
    await page.goto('/app/projects')
    await expect(page.getByRole('article', { name }).getByRole('link', { name: '1 form · 1 draft', exact: true })).toBeVisible()
    await expect(sidebar.getByRole('link', { name: `${name}, new in Forms`, exact: true })).toBeVisible()
  } finally {
    await request.post(`/api/projects/${project.id}/archive`, { headers: { Authorization: `Bearer ${TOKEN}` } })
  }
})
