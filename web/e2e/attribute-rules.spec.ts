import { expect, test, type Page } from '@playwright/test'
import { attributesListed, trackWithAttributes } from './attributes'
import { createProject } from './forms'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'

async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)
}

test('renames, merges, blocks and unblocks attributes from a project settings page', async ({ page, request }) => {
  // A login, a wait for the write buffer's flush and a dozen UI steps.
  test.setTimeout(120_000)
  const headers = { Authorization: `Bearer ${TOKEN}` }
  const { id, key } = await createProject(request, `attributes-${Date.now()}`, [])
  try {
    const sent = await trackWithAttributes(request, key, [
      { name: 'signup', attributes: { planType: 'pro', plan: 'team', debug: '1' } },
      { name: 'signup', attributes: { planType: 'free', plan: 'pro' } },
    ])
    expect(sent.accepted).toBe(2)
    await attributesListed(request, id, ['planType', 'plan', 'debug'])

    await login(page)
    // The default range ends yesterday; these events are from today.
    await page.goto(`/app/projects/${id}/settings?range=today`)
    const attributes = page.getByRole('region', { name: 'Attributes' })
    const rows = attributes.getByRole('table')
    const row = (name: string) => rows.getByRole('row', { name: new RegExp(`\\b${name}\\b`) })
    for (const k of ['planType', 'plan', 'debug']) await expect(row(k)).toBeVisible()

    // Every key breaks down in the event files: no switch, and no declared list on the project.
    await expect(attributes.getByRole('switch')).toHaveCount(0)
    const listed = await request.get('/api/projects', { headers })
    expect(listed.ok(), await listed.text()).toBeTruthy()
    const { projects } = (await listed.json()) as { projects: { project_id: number; attributes?: string[] }[] }
    expect(projects.find((p) => p.project_id === id)?.attributes).toBeUndefined()

    // Rename planType: its stored values move to the new key, which lists the old one as an alias.
    await attributes.getByRole('button', { name: 'Actions for planType' }).click()
    await page.getByRole('menuitem', { name: 'Rename…' }).click()
    await page.getByLabel('New name').fill('plan_type')
    await page.getByRole('button', { name: 'Rename', exact: true }).click()
    await expect(page.getByText('Renamed planType to plan_type')).toBeVisible()
    await expect(row('plan_type').getByRole('button', { name: 'Remove alias planType' })).toBeVisible()
    await expect(attributes.locator('code').filter({ hasText: /^planType$/ })).toHaveCount(0)

    // Merge plan_type into plan, picked from the other attributes: its row is gone and plan lists both old keys.
    await attributes.getByRole('button', { name: 'Actions for plan_type' }).click()
    await page.getByRole('menuitem', { name: 'Merge into…' }).click()
    const merge = page.getByRole('dialog', { name: 'Merge plan_type into…' })
    await merge.getByRole('option', { name: /^plan\b/ }).click()
    await merge.getByRole('button', { name: 'Merge into plan' }).click()
    await expect(page.getByText('Merged plan_type into plan')).toBeVisible()
    await expect(attributes.locator('code').filter({ hasText: /^plan_type$/ })).toHaveCount(0)
    // Aliases follow the chain: planType was renamed to plan_type, which is now merged into plan.
    await expect(row('plan').getByRole('button', { name: 'Remove alias plan_type' })).toBeVisible()
    await expect(row('plan').getByRole('button', { name: 'Remove alias planType' })).toBeVisible()

    // Block debug: confirmed, marked on its row, and stripped on arrival.
    await attributes.getByRole('button', { name: 'Actions for debug' }).click()
    await page.getByRole('menuitem', { name: 'Block…' }).click()
    await expect(page.getByRole('alertdialog', { name: 'Block debug?' })).toBeVisible()
    await page.getByRole('button', { name: 'Block attribute' }).click()
    await expect(page.getByText('Blocked debug')).toBeVisible()
    await expect(row('debug').getByText('Blocked', { exact: true })).toBeVisible()
    await attributes.getByRole('button', { name: 'Actions for debug' }).click()
    await expect(page.getByRole('menuitem', { name: 'Unblock' })).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'Block…' })).toHaveCount(0)
    await page.keyboard.press('Escape')

    // Accepted, so a client does not retry, with a warning for whoever integrates; the other attributes are kept.
    const again = await trackWithAttributes(request, key, [{ name: 'signup', attributes: { debug: '1', plan: 'pro' } }])
    expect(again.accepted).toBe(1)
    expect(again.warnings?.map((w) => w.reason)).toEqual(['attribute "debug" is blocked in this project, dropped'])

    // Unblock debug from its row: the mark goes.
    await attributes.getByRole('button', { name: 'Actions for debug' }).click()
    await page.getByRole('menuitem', { name: 'Unblock' }).click()
    await expect(page.getByText('Removed the rule for debug')).toBeVisible()
    await expect(attributes.getByText('Blocked', { exact: true })).toHaveCount(0)
  } finally {
    // Archived, so later specs' Projects page does not grow by one card per run.
    await request.post(`/api/projects/${id}/archive`, { headers })
  }
})
