import { expect as base, test, type Page } from '@playwright/test'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'
const BUILT_INS = ['Views', 'Product', 'Users', 'Groups', 'Retention', 'Web Vitals', 'Measures']

// Each tab change is a write, which on a loaded host can wait seconds for
// SQLite behind the other spec files' writes and the server's startup pass.
const expect = base.configure({ timeout: 20_000 })

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

test('a project opens on Setup with the built-in tabs beside it; tabs are removed, re-added and chosen from the dashboard', async ({
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
  await expect(page).toHaveURL(/\/app\/projects\/\d+\/setup$/)
  const projectURL = new URL(page.url()).pathname.replace(/\/setup$/, '')
  const tabs = page.getByRole('tablist', { name: 'Tabs' })
  await expect(tabs.getByRole('tab')).toHaveText(['Setup', ...BUILT_INS])

  // A built-in tab, removed from its own menu, comes back to its place from "+".
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
  await expect(tabs.getByRole('tab')).toHaveText(['Setup', ...BUILT_INS])
  await expect(tabs.getByRole('tab', { name: 'Product', exact: true })).toHaveAttribute('data-state', 'active')

  // A dashboard of our own joins after the built-ins.
  await page.getByRole('button', { name: 'Add tab' }).click()
  await page.getByRole('dialog').getByRole('group', { name: 'Your dashboards' }).getByRole('button', { name: title, exact: true }).click()
  await expect(tabs.getByRole('tab')).toHaveText(['Setup', ...BUILT_INS, title])
  await expect(page.getByRole('heading', { level: 1, name: title, exact: true })).toBeVisible()

  // Your own tabs reorder; a phone does it from the tab's menu (a wide
  // screen drags), and the new order is the project's, after a reload too.
  await page.getByRole('button', { name: 'Add tab' }).click()
  await page.getByRole('dialog').getByRole('group', { name: 'Your dashboards' }).getByRole('button', { name: second, exact: true }).click()
  await expect(tabs.getByRole('tab')).toHaveText(['Setup', ...BUILT_INS, title, second])
  await expect(page.getByRole('heading', { level: 1, name: second, exact: true })).toBeVisible()
  await page.setViewportSize({ width: 390, height: 800 })
  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Move left' }).click()
  await page.setViewportSize({ width: 1280, height: 800 })
  await expect(tabs.getByRole('tab')).toHaveText(['Setup', ...BUILT_INS, second, title])
  await page.reload()
  await expect(tabs.getByRole('tab')).toHaveText(['Setup', ...BUILT_INS, second, title])
  // Back to one tab of our own for the rest of the flow.
  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Remove from this project' }).click()
  await expect(page.getByText(`Removed '${second}'`)).toBeVisible()
  await expect(tabs.getByRole('tab')).toHaveText(['Setup', ...BUILT_INS, title])

  await tabs.getByRole('tab', { name: 'Setup', exact: true }).click()
  await expect(page).toHaveURL(/\/setup$/)
  await expect(page.getByRole('region', { name: 'Allowed origins' })).toBeVisible()

  // The dashboard page chooses the projects it is a tab of.
  await page.goto(`/app/dashboards/${mineId}`)
  await page.waitForLoadState('networkidle')
  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Project tabs…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Project tabs' })
  const dev = dialog.getByRole('checkbox', { name: 'dev', exact: true })
  await expect(dev).toBeChecked()
  await dev.click()
  await expect(dev).not.toBeChecked()
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  // Closing the dialog leaves the page usable: the tab menu opens again.
  await page.getByRole('button', { name: 'Tab actions' }).click()
  await expect(page.getByRole('menuitem', { name: 'Project tabs…' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('menuitem', { name: 'Project tabs…' })).toHaveCount(0)

  await page.goto(`${projectURL}/setup`)
  await expect(tabs.getByRole('tab')).toHaveText(['Setup', ...BUILT_INS])

  await page.goto(`/app/dashboards/${mineId}`)
  await page.waitForLoadState('networkidle')
  await page.getByRole('button', { name: 'Tab actions' }).click()
  await page.getByRole('menuitem', { name: 'Project tabs…' }).click()
  await dev.click()
  await expect(dev).toBeChecked()
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)
  await page.getByRole('link', { name: 'Projects', exact: true }).click()
  await page.getByRole('article', { name: 'dev' }).getByRole('link', { name: 'dev' }).click()
  await expect(tabs.getByRole('tab')).toHaveText(['Setup', ...BUILT_INS, title])

  // Archiving our own dashboard takes its tab away. The second goes too:
  // arrange.spec.ts expects no dashboards of ours left in the sidebar.
  const archived = await request.post(`/api/dashboards/${mineId}/archive`, { headers, data: {} })
  expect(archived.ok(), await archived.text()).toBeTruthy()
  const archived2 = await request.post(`/api/dashboards/${secondId}/archive`, { headers, data: {} })
  expect(archived2.ok(), await archived2.text()).toBeTruthy()
  await page.reload()
  await expect(tabs.getByRole('tab', { name: 'Setup', exact: true })).toBeVisible()
  await expect(tabs.getByRole('tab', { name: title, exact: true })).toHaveCount(0)
})
