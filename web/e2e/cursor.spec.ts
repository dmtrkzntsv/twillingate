import { expect, test, type Page } from '@playwright/test'
import { createShare } from './png'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'

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
  // A share of Views' first widget, so the Shares page always has a row's controls to check.
  const headers = { Authorization: 'Bearer e2e-token' }
  const views = await request.get('/api/dashboards/1', { headers })
  expect(views.ok(), await views.text()).toBeTruthy()
  const { widgets } = (await views.json()) as { widgets: { widget_id: number }[] }
  const to = new Date().toISOString().slice(0, 10)
  const from = new Date(Date.now() - 6 * 86_400_000).toISOString().slice(0, 10)
  await createShare(request, { widgetId: widgets[0].widget_id, projectId: 1, from, to })

  await login(page)
  for (const path of ['/app/projects', '/app/projects/1', '/app/dashboards', '/app/archive', '/app/shares', '/app/gallery/components', '/app/gallery/dashboards']) {
    await page.goto(path)
    await page.waitForLoadState('networkidle')
    await expect(page.locator('main, [data-slot="sidebar-inset"]').first()).toBeVisible()
    expect(await withoutPointer(page), path).toEqual([])
  }
})
