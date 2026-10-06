import { expect, test, type Page } from '@playwright/test'

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
  await login(page)
  for (const path of ['/app/projects', '/app/projects/1/setup', '/app/dashboards', '/app/archive', '/app/gallery/components', '/app/gallery/dashboards']) {
    await page.goto(path)
    await page.waitForLoadState('networkidle')
    await expect(page.locator('main, [data-slot="sidebar-inset"]').first()).toBeVisible()
    expect(await withoutPointer(page), path).toEqual([])
  }

  // A project's "+" and the dashboards its picker offers, on a dashboard
  // tab; one of our own, so the picker is never empty.
  const headers = { Authorization: `Bearer ${TOKEN}` }
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
