import { readFileSync } from 'node:fs'
import { expect, test, type Page } from '@playwright/test'
import { pngSize } from './png'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN.
const PASSWORD = 'e2e-pass'

async function signIn(page: Page): Promise<void> {
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
}

for (const scheme of ['light', 'dark'] as const) {
  test(`every component's first card downloads at 1200x630 and 2400x1260 (${scheme})`, async ({ page }, testInfo) => {
    // Eighteen components, two captures each.
    test.setTimeout(360_000)
    await page.emulateMedia({ colorScheme: scheme })
    await page.goto('/app/gallery/components?view=cards')
    await signIn(page)
    await page.waitForURL(/\/app\/gallery\/components/)
    await page.goto('/app/gallery/components?view=cards')

    const sections = page.locator('section[id^="component-"]')
    await expect(sections.first()).toBeVisible()
    const names = (await sections.evaluateAll((els) => els.map((el) => el.id.replace(/^component-/, '')))).filter(Boolean)
    // Every component the gallery lists (gallery.spec.ts's NAMES has eighteen); a missing section fails here.
    expect(names.length).toBeGreaterThanOrEqual(18)

    for (const name of names) {
      const section = page.locator(`#component-${name}`)
      await section.scrollIntoViewIfNeeded()
      for (const ratio of [1, 2] as const) {
        const button = section.getByTestId(`card-png-${ratio}x`).first()
        const [download] = await Promise.all([page.waitForEvent('download', { timeout: 60_000 }), button.click()])
        const bytes = readFileSync(await download.path())
        expect(pngSize(bytes), `${name} ${ratio}x`).toEqual(ratio === 1 ? { width: 1200, height: 630 } : { width: 2400, height: 1260 })
        if (ratio === 1) expect(bytes.length, `${name} 1x size`).toBeLessThan(300 * 1024)
        else await testInfo.attach(`${name}-${scheme}-2x.png`, { body: bytes, contentType: 'image/png' })
      }
    }
  })
}
