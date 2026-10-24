import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
// Copied from arrange.spec.ts rather than imported across spec files.
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

/**
 * A project of this file's own, over REST: other spec files count the
 * seeded project's tabs, which the dashboard made here would add to.
 */
async function createProject(request: APIRequestContext, name: string): Promise<number> {
  const res = await request.post('/api/projects', { headers: authHeaders(), data: { name, skip_key: true } })
  expect(res.ok(), await res.text()).toBeTruthy()
  return ((await res.json()) as { project_id: number }).project_id
}

/** Opens a card's "…" menu: its button shows on hover. */
async function cardMenu(page: Page, title: string): Promise<void> {
  const card = page.locator('[data-slot="widget-card"]', { hasText: title })
  await card.hover()
  await card.getByRole('button', { name: 'Widget actions' }).click()
}

/** Replaces the SQL editor's whole text. */
async function typeSQL(page: Page, sql: string): Promise<void> {
  await page.getByRole('dialog').getByLabel('SQL').click()
  await page.keyboard.press('ControlOrMeta+a')
  await page.keyboard.insertText(sql)
}

test('builds a dashboard from a project and edits its widget', async ({ page, request }) => {
  test.setTimeout(90_000)
  const stamp = Date.now()
  const projectId = await createProject(request, `Editor ${stamp}`)
  const title = `Editor flow ${stamp}`
  try {
    await login(page)
    await page.goto(`/app/projects/${projectId}`)
    await page.getByRole('button', { name: 'Add tab' }).click()
    await page.getByRole('button', { name: 'New dashboard…' }).click()
    await page.getByLabel('Dashboard title').fill(title)
    await page.getByRole('button', { name: 'Create' }).click()
    // The project opens on its first tab (a built-in dashboard's URL): wait for the new one's.
    await expect(page.getByRole('heading', { level: 1, name: title })).toBeVisible()
    await expect(page).toHaveURL(new RegExp(`/projects/${projectId}/dashboards/\\d+$`))
    const dashboardUrl = new URL(page.url()).pathname

    // Add a line widget: pick it, write the query, run it with the keys, save.
    await page.getByRole('button', { name: 'Add widget' }).first().click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByRole('heading', { name: 'Add widget' })).toBeVisible()
    await dialog.getByLabel('Title').fill('Daily views')
    await dialog.getByRole('combobox', { name: 'Visualization' }).click()
    await page.getByRole('option', { name: 'line', exact: true }).click()
    await typeSQL(
      page,
      'SELECT day AS x, count(DISTINCT actor_id) AS y FROM views WHERE project_id = :project AND day BETWEEN :from AND :to GROUP BY day'
    )
    // The query reads :project and :from/:to, so the editor offers both.
    await expect(dialog.getByRole('combobox', { name: 'Project' })).toBeVisible()
    await expect(dialog.getByRole('button', { name: /^Range: / })).toBeVisible()
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(dialog.getByText(/^\d+ rows? · \d+ ms/)).toBeVisible()
    // The project is this test's own and holds no views: both halves say so.
    await expect(dialog.getByRole('region', { name: 'Preview' }).getByText('The query returned no rows')).toBeVisible()
    await expect(dialog.getByRole('region', { name: 'Rows' }).getByRole('cell', { name: 'No rows' })).toBeVisible()
    await dialog.getByRole('button', { name: 'Save' }).click()
    await expect(dialog).toBeHidden()
    const card = page.locator('[data-slot="widget-card"]', { hasText: 'Daily views' })
    await expect(card).toBeVisible()

    // Edit the SQL: a run whose columns don't fit the line shows the problem with the rows.
    await cardMenu(page, 'Daily views')
    await page.getByRole('menuitem', { name: 'Edit…' }).click()
    await expect(dialog.getByRole('heading', { name: 'Edit widget' })).toBeVisible()
    // The stored query runs as the editor opens: the preview needs no click.
    await expect(dialog.getByText(/^\d+ rows? · \d+ ms/)).toBeVisible()
    // The halves and the work surfaces resize by their lines, here from the keyboard.
    const halves = dialog.getByRole('separator', { name: 'Resize the halves' })
    const preview = dialog.getByRole('region', { name: 'Preview' })
    const before = (await preview.boundingBox())!.width
    await halves.focus()
    await page.keyboard.press('ArrowRight')
    await expect(halves).toHaveAttribute('aria-valuenow', '52')
    await expect.poll(async () => (await preview.boundingBox())!.width).toBeGreaterThan(before)
    const surfaces = dialog.getByRole('separator', { name: 'Resize the work surfaces' })
    const tall = Number(await surfaces.getAttribute('aria-valuenow'))
    await surfaces.focus()
    await page.keyboard.press('ArrowUp')
    await expect(surfaces).toHaveAttribute('aria-valuenow', String(tall - 16))
    await surfaces.dblclick()
    await expect(surfaces).toHaveAttribute('aria-valuenow', String(tall))
    await halves.dblclick()
    await typeSQL(page, 'SELECT day AS x FROM views WHERE project_id = :project LIMIT 3')
    await expect(dialog.getByRole('button', { name: /^Range: / })).toHaveCount(0)
    await dialog.getByRole('button', { name: /Run query/ }).click()
    await expect(dialog.getByText(/line needs y/)).toBeVisible()
    await typeSQL(page, 'SELECT day AS x, count(*) AS y FROM views WHERE project_id = :project GROUP BY day LIMIT 3')
    await expect(dialog.getByText('Changed since last run')).toBeVisible()
    await dialog.getByRole('button', { name: 'Save' }).click()
    await expect(dialog).toBeHidden()
    await expect(card).toBeVisible()

    // Rename in place.
    await cardMenu(page, 'Daily views')
    await page.getByRole('menuitem', { name: 'Rename' }).click()
    await page.getByLabel('Widget title').fill('Views per day')
    await page.keyboard.press('Enter')
    const renamed = page.locator('[data-slot="widget-card"]', { hasText: 'Views per day' })
    await expect(renamed).toBeVisible()
    await expect(card).toHaveCount(0)

    // Archive, undo, archive again, then restore it from the Archive page.
    await cardMenu(page, 'Views per day')
    await page.getByRole('menuitem', { name: 'Archive' }).click()
    await expect(renamed).toBeHidden()
    await expect(page.getByText('Widget archived')).toBeVisible()
    await page.getByRole('button', { name: 'Undo' }).click()
    await expect(renamed).toBeVisible()
    await cardMenu(page, 'Views per day')
    await page.getByRole('menuitem', { name: 'Archive' }).click()
    await expect(renamed).toBeHidden()

    await page.goto('/app/archive')
    // Other spec files archive widgets of their own on this server: only this dashboard's row.
    const group = page.getByRole('region', { name: 'Archived widgets' }).getByRole('listitem', { name: title })
    await group.getByRole('listitem').filter({ hasText: 'Views per day' }).getByRole('button', { name: 'Restore' }).click()
    await expect(group).toHaveCount(0)
    await page.goto(dashboardUrl)
    await expect(page.locator('[data-slot="widget-card"]', { hasText: 'Views per day' })).toBeVisible()
  } finally {
    await request.post(`/api/projects/${projectId}/archive`, { headers: authHeaders() })
  }
})

test('duplicates a widget and copies a built-in one onto a dashboard of its own', async ({ page, request }) => {
  test.setTimeout(60_000)
  const stamp = Date.now()
  const title = `Copies ${stamp}`
  const note = `Note ${stamp}`
  const res = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: { title, range: '7d', widgets: [{ component: 'markdown', title: note, source: { type: 'md', content: 'Hello' }, width: 6, height: 3 }] },
  })
  expect(res.ok(), await res.text()).toBeTruthy()
  const id = ((await res.json()) as { dashboard_id: number }).dashboard_id
  const widgets = async () => {
    const r = await request.get(`/api/dashboards/${id}`, { headers: authHeaders() })
    return ((await r.json()) as { widgets: { name: string }[] }).widgets.map((w) => w.name)
  }
  try {
    const list = await request.get('/api/dashboards', { headers: authHeaders() })
    const builtIn = ((await list.json()) as { dashboards: { dashboard_id: number; owner: string; widgets: number }[] }).dashboards.find(
      (d) => d.owner === 'system' && d.widgets > 0
    )!

    await login(page)
    await page.goto(`/app/dashboards/${builtIn.dashboard_id}`)
    const first = page.locator('[data-slot="widget-card"]').first()
    await first.hover()
    await first.getByRole('button', { name: 'Widget actions' }).click()
    // A built-in widget is copied out, never duplicated in place.
    await expect(page.getByRole('menuitem', { name: 'Duplicate' })).toHaveCount(0)
    await page.getByRole('menuitem', { name: 'Copy to dashboard…' }).click()
    const dialog = page.getByRole('dialog', { name: 'Copy to dashboard' })
    await dialog.getByRole('button', { name: title, exact: true }).click()
    await expect(dialog).toBeHidden()
    await expect(page.getByText(`Copied to ${title}`)).toBeVisible()
    await page.getByRole('button', { name: 'Open' }).click()
    await expect(page).toHaveURL(new RegExp(`/dashboards/${id}$`))
    await expect(page.locator('[data-slot="widget-card"]')).toHaveCount(2)

    // The duplicate lands right after the original, under a name of its own.
    await cardMenu(page, note)
    await page.getByRole('menuitem', { name: 'Duplicate' }).click()
    await expect(page.locator('[data-slot="widget-card"]', { hasText: note })).toHaveCount(2)
    const names = await widgets()
    expect(names).toHaveLength(3)
    expect(names[1]).toMatch(new RegExp(`^${names[0]}-2$`))
  } finally {
    await request.post(`/api/dashboards/${id}/archive`, { headers: authHeaders() })
  }
})

test('adds a stat whose source is a structured query', async ({ page, request }) => {
  test.setTimeout(60_000)
  const stamp = Date.now()
  const title = `Query ${stamp}`
  const res = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: { title, range: '30d', widgets: [{ component: 'markdown', title: `Note ${stamp}`, source: { type: 'md', content: 'Hello' }, width: 6, height: 3 }] },
  })
  expect(res.ok(), await res.text()).toBeTruthy()
  const id = ((await res.json()) as { dashboard_id: number }).dashboard_id
  try {
    await login(page)
    await page.goto(`/app/dashboards/${id}`)
    await page.getByRole('button', { name: 'Add widget' }).first().click()
    const dialog = page.getByRole('dialog')
    await dialog.getByLabel('Title').fill('Views by query')
    await dialog.getByRole('combobox', { name: 'Visualization' }).click()
    await page.getByRole('option', { name: 'stat', exact: true }).click()
    // A new widget starts on SQL; Query starts from a stat over the views, renamed to value.
    await expect(dialog.getByRole('button', { name: 'SQL', exact: true })).toHaveAttribute('aria-pressed', 'true')
    await dialog.getByRole('button', { name: 'Query', exact: true }).click()
    await expect(dialog.getByLabel('Query')).toContainText('"kind": "stat"')
    // A query follows the project and the range: both switchers show. The seeded project holds views.
    await dialog.getByRole('combobox', { name: 'Project' }).click()
    await page.getByRole('option', { name: 'dev', exact: true }).click()
    await expect(dialog.getByRole('button', { name: /^Range: / })).toBeVisible()
    await dialog.getByRole('button', { name: /Run query/ }).click()
    await expect(dialog.getByText(/^1 row · \d+ ms/)).toBeVisible()
    await expect(dialog.getByRole('region', { name: 'Preview' })).toContainText(/[1-9]/)
    await dialog.getByRole('button', { name: 'Save' }).click()
    await expect(dialog).toBeHidden()
    const card = page.locator('[data-slot="widget-card"]', { hasText: 'Views by query' })
    await expect(card).toContainText(/\d/)

    // It saved as a query, and opens on its query again.
    const got = await request.get(`/api/dashboards/${id}`, { headers: authHeaders() })
    const widgets = ((await got.json()) as { widgets: { title?: string; source: { type: string } }[] }).widgets
    expect(widgets.find((w) => w.title === 'Views by query')?.source.type).toBe('query')
    await cardMenu(page, 'Views by query')
    await page.getByRole('menuitem', { name: 'Edit…' }).click()
    await expect(dialog.getByRole('button', { name: 'Query', exact: true })).toHaveAttribute('aria-pressed', 'true')
    await expect(dialog.getByLabel('Query')).toContainText('"rename"')
  } finally {
    await request.post(`/api/dashboards/${id}/archive`, { headers: authHeaders() })
  }
})
