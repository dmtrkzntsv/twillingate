import { readFileSync } from 'node:fs'
import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { pngSize } from './png'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'
const ORIGIN = 'http://127.0.0.1:18080'

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

/** A share's title as the API reports it (the card on screen may cut it). */
async function shareTitle(request: APIRequestContext, id: string): Promise<string> {
  const res = await request.get('/api/widget-shares', { headers: authHeaders() })
  expect(res.ok(), await res.text()).toBeTruthy()
  const { shares } = (await res.json()) as { shares: { id: string; title: string }[] }
  const found = shares.find((s) => s.id === id)
  expect(found, `share ${id} listed`).toBeDefined()
  return found!.title
}

/** Opens a widget card's "Widget actions" menu and chooses `item`. */
async function chooseAction(page: Page, card: ReturnType<Page['locator']>, item: 'Share…' | 'Download PNG'): Promise<void> {
  await card.hover()
  await card.getByRole('button', { name: 'Widget actions' }).click()
  const entry = page.getByRole('menuitem', { name: item })
  // Both wait for the widget's answer: the card has to be drawable.
  await expect(entry).toBeEnabled()
  await entry.click()
}

/** From the open Share dialog: waits for the preview, keeps one month, creates the link and returns it. */
async function createLink(page: Page): Promise<string> {
  const dialog = page.getByRole('dialog', { name: 'Share widget' })
  await expect(dialog.getByRole('img', { name: 'Preview of the share card' })).toBeVisible({ timeout: 30_000 })
  await expect(dialog.getByLabel('Archive after')).toHaveValue('30d')
  await dialog.getByRole('button', { name: 'Create link' }).click()
  const link = dialog.getByRole('textbox', { name: 'Share link' })
  await expect(link).toBeVisible()
  return link.inputValue()
}

test('share a widget, see its public page, archive it, restore it, download it', async ({ page, browser, request }) => {
  test.setTimeout(120_000)
  await login(page)
  await page.goto('/app/dashboards/1')
  await page.waitForLoadState('networkidle')

  const card = page.locator('[data-slot="widget-card"]').first()
  await chooseAction(page, card, 'Share…')
  const link = await createLink(page)
  expect(link).toMatch(/^http:\/\/127\.0\.0\.1:18080\/share\/[0-9a-f-]{36}$/)
  const id = link.split('/').pop()!
  const title = await shareTitle(request, id)

  // The public page: no sign-in, a picture and a footer.
  const anon = await browser.newContext()
  const pub = await anon.newPage()
  const res = await pub.goto(link)
  expect(res?.status()).toBe(200)
  expect(await pub.locator('meta[property="og:image"]').getAttribute('content')).toMatch(/\.png$/)
  expect(await pub.locator('meta[name="twitter:card"]').getAttribute('content')).toBe('summary_large_image')
  await expect(pub.locator('a[href="https://twillingate.dev"]')).toHaveText('twillingate.dev')
  await expect.poll(() => pub.locator('main img').evaluate((img: HTMLImageElement) => img.naturalWidth)).toBe(2400)
  await anon.close()

  // The 1x picture the link preview fetches.
  const png = await request.get(`${link}.png`)
  expect(png.status()).toBe(200)
  expect(png.headers()['content-type']).toBe('image/png')
  const body = await png.body()
  expect(pngSize(body)).toEqual({ width: 1200, height: 630 })
  expect(body.length).toBeLessThan(300 * 1024)

  // Shares page: archive it, and the link answers 404.
  await page.goto('/app/shares')
  const row = page.getByRole('row').filter({ has: page.locator(`a[href="${link}"]`) })
  await expect(row).toBeVisible()
  await expect(row).toContainText(title)
  await row.getByRole('button', { name: 'Archive' }).click()
  await expect(row).toHaveCount(0)
  expect((await request.get(link)).status()).toBe(404)

  // Archive page: restore it with the default date, and the link answers again.
  await page.goto('/app/archive')
  const archived = page.getByRole('region', { name: 'Shares' }).getByRole('listitem').filter({ hasText: title })
  await expect(archived).toBeVisible()
  await archived.getByRole('button', { name: 'Restore' }).click()
  const restore = page.getByRole('dialog', { name: 'Restore share' })
  await expect(restore.getByLabel('Archive after')).toHaveValue('30d')
  await restore.getByRole('button', { name: 'Restore' }).click()
  await expect(restore).toHaveCount(0)
  await expect.poll(async () => (await request.get(link)).status()).toBe(200)

  // Download PNG: the 2x card, named after the widget and its range.
  await page.goto('/app/dashboards/1')
  await page.waitForLoadState('networkidle')
  const download = page.waitForEvent('download')
  await chooseAction(page, page.locator('[data-slot="widget-card"]').first(), 'Download PNG')
  const file = await download
  expect(file.suggestedFilename()).toMatch(/-\d{4}-\d{2}-\d{2}-\d{4}-\d{2}-\d{2}\.png$/)
  expect(pngSize(readFileSync(await file.path()))).toEqual({ width: 2400, height: 1260 })
})

test('a 120-character title is cut to two lines on a cold page, in the card and in the image', async ({ browser, request }) => {
  test.setTimeout(120_000)
  // A stat that follows the project and range, so its card has a project to share from.
  const created = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: {
      title: 'Share title fit',
      range: '7d',
      widgets: [
        {
          component: 'stat',
          title: 'short',
          source: { type: 'sql', content: 'SELECT 42 + 0 * :project AS value WHERE :from <= :to' },
          width: 6,
          height: 4,
        },
      ],
    },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const { dashboard_id: dashboardId } = (await created.json()) as { dashboard_id: number }
  const got = await request.get(`/api/dashboards/${dashboardId}`, { headers: authHeaders() })
  const { widgets } = (await got.json()) as { widgets: { widget_id: number }[] }

  // Words, so the cut falls on one; exactly 120 characters.
  const title = `${Date.now()} ${'quarterly revenue by region and plan '.repeat(4)}`.slice(0, 120).replace(/ $/, 'x')
  expect(title).toHaveLength(120)
  const renamed = await request.patch(`/api/widgets/${widgets[0].widget_id}`, { headers: authHeaders(), data: { title } })
  expect(renamed.ok(), await renamed.text()).toBeTruthy()

  // A context of its own, so nothing (fonts included) is cached from another test.
  const context = await browser.newContext({ baseURL: ORIGIN, viewport: { width: 1280, height: 800 } })
  const page = await context.newPage()
  // The card is drawn out of sight and goes once captured, so its title is read
  // every frame it is there; what stays is the last look before it went.
  await page.addInitScript(() => {
    const w = window as unknown as { __cardTitle?: { text: string; lines: number } }
    const look = () => {
      const h2 = document.querySelector('[data-share-card] h2')
      if (h2) {
        const range = document.createRange()
        range.selectNodeContents(h2)
        const tops = new Set(Array.from(range.getClientRects()).map((r) => Math.round(r.top)))
        w.__cardTitle = { text: h2.textContent ?? '', lines: tops.size }
      }
      requestAnimationFrame(look)
    }
    requestAnimationFrame(look)
  })
  await login(page)
  await page.goto(`/app/dashboards/${dashboardId}`)
  await page.waitForLoadState('networkidle')
  await chooseAction(page, page.locator('[data-slot="widget-card"]').first(), 'Share…')
  const link = await createLink(page)

  const seen = await page.evaluate(() => (window as unknown as { __cardTitle?: { text: string; lines: number } }).__cardTitle)
  expect(seen, 'the card was drawn').toBeDefined()
  expect(seen!.lines).toBeLessThanOrEqual(2)
  expect(seen!.text.endsWith('…')).toBe(true)
  expect(seen!.text.length).toBeLessThan(title.length)
  await context.close()

  const png = await request.get(`${link}.png`)
  expect(png.status()).toBe(200)
  expect(pngSize(await png.body())).toEqual({ width: 1200, height: 630 })
})
