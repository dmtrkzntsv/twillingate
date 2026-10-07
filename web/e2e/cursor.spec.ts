import { expect, test, type Page } from '@playwright/test'
import { createProject, submitJSON } from './forms'
import { createShare } from './png'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'

// Every clickable thing shows the hand: src/index.css sets it once for
// these, in the base layer, and this fails on a visible, enabled control
// that ends up with another cursor. A deliberate exception goes in ALLOWED
// with the reason: a drag handle's grab, and the sidebar's edge rail, whose
// resize cursor says it toggles the sidebar by its edge.
const CLICKABLE = [
  "button:not(:disabled):not([aria-disabled='true'])",
  "a[href]:not([aria-disabled='true'])",
  'summary',
  'select:not(:disabled)',
  'label[for]',
  "input[type='checkbox']:not(:disabled)",
  "input[type='radio']:not(:disabled)",
  "[role='button']:not([aria-disabled='true'])",
  "[role='tab']:not([aria-disabled='true'])",
  "[role='menuitem']:not([data-disabled])",
  "[role='switch']:not([aria-disabled='true'])",
  "[role='checkbox']:not([aria-disabled='true'])",
  "[role='combobox']:not([aria-disabled='true'])",
].join(', ')
const ALLOWED = new Set(['grab', 'grabbing', 'w-resize', 'e-resize', 'ew-resize', 'col-resize'])

async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)
}

/** The visible clickable elements on the page whose cursor is not the pointer. */
async function withoutPointer(page: Page): Promise<string[]> {
  return page.evaluate(
    ({ selector, allowed }) =>
      Array.from(document.querySelectorAll<HTMLElement>(selector))
        .filter((el) => {
          const r = el.getBoundingClientRect()
          const style = getComputedStyle(el)
          return r.width > 0 && r.height > 0 && style.visibility !== 'hidden' && style.pointerEvents !== 'none'
        })
        .filter((el) => {
          const cursor = getComputedStyle(el).cursor
          return cursor !== 'pointer' && !allowed.includes(cursor)
        })
        .map((el) => `${el.tagName.toLowerCase()} "${(el.getAttribute('aria-label') ?? el.textContent ?? '').trim().slice(0, 40)}": ${getComputedStyle(el).cursor}`),
    { selector: CLICKABLE, allowed: [...ALLOWED] }
  )
}

test('every link and button shows the pointer', async ({ page, request }) => {
  test.setTimeout(90_000)
  // A share of Views' first widget, so the Shares page always has a row's controls to check.
  const headers = { Authorization: `Bearer ${TOKEN}` }
  const views = await request.get('/api/dashboards/1', { headers })
  expect(views.ok(), await views.text()).toBeTruthy()
  const { widgets } = (await views.json()) as { widgets: { widget_id: number }[] }
  const to = new Date().toISOString().slice(0, 10)
  const from = new Date(Date.now() - 6 * 86_400_000).toISOString().slice(0, 10)
  await createShare(request, { widgetId: widgets[0].widget_id, projectId: 1, from, to })

  // A project with a draft form, for the Forms tab's rows and a form's page and table.
  const forms = await createProject(request, `cursor-forms-${Date.now()}`, [])
  await submitJSON(request, forms.key, 'contact', { email: 'ann@example.com', message: 'Hello' })

  await login(page)
  for (const path of [
    '/app/projects',
    '/app/projects/1/setup',
    `/app/projects/${forms.id}/forms`,
    `/app/projects/${forms.id}/forms/contact`,
    '/app/dashboards',
    '/app/archive',
    '/app/shares',
    '/app/gallery/components',
    '/app/gallery/dashboards',
  ]) {
    await page.goto(path)
    await page.waitForLoadState('networkidle')
    await expect(page.locator('main, [data-slot="sidebar-inset"]').first()).toBeVisible()
    expect(await withoutPointer(page), path).toEqual([])
  }

  // The empty Forms tab's copy buttons, a form row's menu, the Approve
  // dialog's checkboxes, and a submission's drawer.
  const empty = await createProject(request, `cursor-no-forms-${Date.now()}`, [])
  await page.goto(`/app/projects/${empty.id}/forms`)
  await expect(page.getByRole('region', { name: 'Add a form' })).toBeVisible()
  expect(await withoutPointer(page), 'no forms').toEqual([])
  await page.goto(`/app/projects/${forms.id}/forms`)
  await page.getByRole('button', { name: 'Actions for contact' }).click()
  await expect(page.getByRole('menuitem', { name: 'Approve…' })).toBeVisible()
  expect(await withoutPointer(page), 'form menu').toEqual([])
  await page.getByRole('menuitem', { name: 'Approve…' }).click()
  await expect(page.getByRole('dialog', { name: 'Approve contact' }).getByRole('checkbox', { name: 'email' })).toBeVisible()
  expect(await withoutPointer(page), 'approve dialog').toEqual([])
  await page.keyboard.press('Escape')
  await page.goto(`/app/projects/${forms.id}/forms/contact`)
  await page.locator('tbody tr').first().click()
  await expect(page.getByRole('dialog', { name: 'Submission' }).getByText('ann@example.com')).toBeVisible()
  expect(await withoutPointer(page), 'submission drawer').toEqual([])
  await page.keyboard.press('Escape')

  // The Share dialog's controls, before and after the link is made (Copy link, Copy embed code, Open).
  await page.goto('/app/dashboards/1')
  await page.waitForLoadState('networkidle')
  const card = page.locator('[data-slot="widget-card"]').first()
  await card.hover()
  await card.getByRole('button', { name: 'Widget actions' }).click()
  await expect(page.getByRole('menuitem', { name: 'Share…' })).toBeEnabled()
  expect(await withoutPointer(page), 'widget menu').toEqual([])
  await page.getByRole('menuitem', { name: 'Share…' }).click()
  const dialog = page.getByRole('dialog', { name: 'Share widget' })
  await expect(dialog.getByRole('button', { name: 'Create link' })).toBeEnabled({ timeout: 30_000 })
  expect(await withoutPointer(page), 'Share dialog').toEqual([])
  await dialog.getByRole('button', { name: 'Create link' }).click()
  await expect(dialog.getByRole('link', { name: 'Open' })).toBeVisible()
  expect(await withoutPointer(page), 'Share dialog, link made').toEqual([])
  // A project's "+" and the dashboards its picker offers, on a dashboard
  // tab; one of our own, so the picker is never empty.
  const title = `E2E cursor ${Date.now()}`
  const created = await request.post('/api/dashboards', { headers, data: { title, range: '7d' } })
  expect(created.ok(), await created.text()).toBeTruthy()
  const { dashboard_id: id } = (await created.json()) as { dashboard_id: number }
  try {
    await page.goto('/app/projects/1/dashboards/1')
    await page.waitForLoadState('networkidle')
    await page.getByRole('button', { name: 'Add tab' }).click()
    await expect(page.getByRole('dialog', { name: 'Add tab' }).getByRole('button', { name: title })).toBeVisible()
    expect(await withoutPointer(page), 'add tab').toEqual([])
  } finally {
    await request.post(`/api/dashboards/${id}/archive`, { headers, data: {} })
  }
})
