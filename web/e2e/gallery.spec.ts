import { expect, test, type Page } from '@playwright/test'

// Matches web/e2e/serve.sh's API_AUTH_DSN.
const PASSWORD = 'e2e-pass'
// Importing ../src/components/widgets here pulls React/Recharts (and
// world-atlas's JSON import) into Node and fails to load, so this is a
// literal copy of Object.keys(widgets).sort() from
// web/src/components/widgets/index.ts.
const NAMES = [
  'area',
  'bar',
  'bar_list',
  'calendar',
  'combo',
  'funnel',
  'heatmap',
  'line',
  'map',
  'markdown',
  'pie',
  'radar',
  'radial',
  'scatter',
  'stat',
  'table',
  'treemap',
]

/** Signs in through the password page from wherever the app bounced to it. */
async function signIn(page: Page): Promise<void> {
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
}

test('a signed-out deep link comes back to the gallery after sign-in', async ({ page }) => {
  await page.goto('/app/gallery/components')
  await signIn(page)
  await page.waitForURL(/\/app\/gallery\/components/)
  await expect(page.getByRole('heading', { level: 1, name: 'Components' })).toBeVisible()
})

test('/app/gallery redirects to the components', async ({ page }) => {
  await page.goto('/app/gallery')
  await signIn(page)
  await page.waitForURL(/\/app\/gallery\/components/)
})

for (const scheme of ['light', 'dark'] as const) {
  test(`every component renders (${scheme})`, async ({ page }, testInfo) => {
    await page.emulateMedia({ colorScheme: scheme })
    await page.goto('/app/gallery/components')
    await signIn(page)
    await page.waitForURL(/\/app\/gallery\/components/)
    for (const name of NAMES) {
      const section = page.locator(`#component-${name}`)
      await section.scrollIntoViewIfNeeded()
      const card = section.locator('[data-slot="widget-card"]').first()
      await expect(card, name).toBeVisible()
      const body = card.locator('[data-slot="widget-body"]')
      await expect(body, name).toBeVisible()
      // Not blank: drawn content with real height, not an empty body.
      const drawn = await body.evaluate((el) => ({
        children: el.querySelectorAll('*').length,
        height: el.firstElementChild?.getBoundingClientRect().height ?? 0,
      }))
      expect(drawn.children, name).toBeGreaterThan(3)
      expect(drawn.height, name).toBeGreaterThan(40)
    }
    await testInfo.attach(`gallery-${scheme}.png`, {
      body: await page.screenshot({ fullPage: true }),
      contentType: 'image/png',
    })
  })
}

test('fits a phone without sideways scrolling', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/app/gallery/components')
  await signIn(page)
  await page.waitForURL(/\/app\/gallery\/components/)
  await page.locator('#component-table').getByRole('button', { name: 'Contract' }).click()
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth)
  expect(overflow).toBeLessThanOrEqual(0)
})

test('the sidebar opens the gallery', async ({ page }) => {
  await page.goto('/app/')
  await signIn(page)
  await page.waitForURL(/\/app\/dashboards\/\d+/)
  await page.getByRole('link', { name: 'Components' }).click()
  await page.waitForURL(/\/app\/gallery\/components/)
})
