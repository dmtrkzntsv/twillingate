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
      'SELECT day AS x, SUM(views) AS y FROM v_views_daily WHERE project_id = :project AND day BETWEEN :from AND :to GROUP BY day'
    )
    // The query reads :project and :from/:to, so the editor offers both.
    await expect(dialog.getByRole('combobox', { name: 'Project' })).toBeVisible()
    await expect(dialog.getByRole('button', { name: /^Range: / })).toBeVisible()
    await page.keyboard.press('ControlOrMeta+Enter')
    await expect(dialog.getByText(/^\d+ rows? · \d+ ms/)).toBeVisible()
    await expect(dialog.getByRole('tab', { name: 'Preview' })).toHaveAttribute('aria-selected', 'true')
    await dialog.getByRole('button', { name: 'Save' }).click()
    await expect(dialog).toBeHidden()
    const card = page.locator('[data-slot="widget-card"]', { hasText: 'Daily views' })
    await expect(card).toBeVisible()

    // Edit the SQL: a run whose columns don't fit the line shows the problem with the rows.
    await cardMenu(page, 'Daily views')
    await page.getByRole('menuitem', { name: 'Edit…' }).click()
    await expect(dialog.getByRole('heading', { name: 'Edit widget' })).toBeVisible()
    await typeSQL(page, 'SELECT day AS x FROM v_views_daily WHERE project_id = :project LIMIT 3')
    await expect(dialog.getByRole('button', { name: /^Range: / })).toHaveCount(0)
    await dialog.getByRole('button', { name: /Run query/ }).click()
    await expect(dialog.getByText(/line needs y/)).toBeVisible()
    await typeSQL(page, 'SELECT day AS x, views AS y FROM v_views_daily WHERE project_id = :project LIMIT 3')
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
