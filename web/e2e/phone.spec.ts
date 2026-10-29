import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { attributesListed } from './attributes'
import { submitJSON } from './forms'
import { createShare } from './png'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const TOKEN = 'e2e-token'

// The narrowest common phone. Nothing on any page scrolls sideways at this
// width: not the page, and not a section inside it. The one exception is a
// widget's own content (a wide data table), which scrolls inside its card.
const PHONE = { width: 360, height: 740 }

function authHeaders() {
  return { Authorization: `Bearer ${TOKEN}` }
}

/**
 * What scrolls sideways: the page itself, or any element outside a widget
 * card. `allowed` is a selector for other boxes that may, as a widget's own
 * content does (the editor's result table).
 */
async function sidewaysScroll(page: Page, allowed?: string): Promise<string[]> {
  return page.evaluate((allowed) => {
    const out: string[] = []
    const root = document.documentElement
    if (root.scrollWidth > root.clientWidth) out.push(`page: ${root.scrollWidth}px wide in ${root.clientWidth}px`)
    for (const el of Array.from(document.querySelectorAll<HTMLElement>('body *'))) {
      if (el.closest('[data-slot="widget-card"]')) continue
      if (allowed && el.matches(allowed)) continue
      const x = getComputedStyle(el).overflowX
      if ((x === 'auto' || x === 'scroll') && el.scrollWidth > el.clientWidth + 1) {
        const name = el.getAttribute('aria-label') ?? el.closest('[aria-label]')?.getAttribute('aria-label') ?? ''
        out.push(`${el.tagName.toLowerCase()} in "${name}": ${el.scrollWidth}px wide in ${el.clientWidth}px`)
      }
    }
    return out
  }, allowed)
}

async function check(page: Page, path: string): Promise<void> {
  await page.goto(path)
  await page.waitForLoadState('networkidle')
  await expect(page.locator('main, [data-slot="sidebar-inset"]').first()).toBeVisible()
  expect(await sidewaysScroll(page), path).toEqual([])
}

async function dashboardIds(request: APIRequestContext): Promise<number[]> {
  const res = await request.get('/api/dashboards', { headers: authHeaders() })
  expect(res.ok(), await res.text()).toBeTruthy()
  const body = (await res.json()) as { dashboards: { dashboard_id: number }[] }
  return body.dashboards.map((d) => d.dashboard_id)
}

/**
 * Whether the element whose own text matches is cut off: wider than its own
 * box, past the viewport's edge, or outside an ancestor that clips (overflow
 * other than visible). Returns what is wrong, empty when it shows whole.
 * `within` is text of the row to look in.
 */
async function clipped(page: Page, text: RegExp, within: string): Promise<string[]> {
  return page.evaluate(
    ({ source, flags, within }) => {
      const re = new RegExp(source, flags)
      // The innermost element whose text matches (a line may hold a span, as
      // the purge date does), only inside the list item that holds `within`:
      // another spec's rows are not this one's.
      const el = Array.from(document.querySelectorAll<HTMLElement>('li *')).find(
        (e) =>
          re.test(e.textContent ?? '') &&
          !Array.from(e.children).some((c) => re.test(c.textContent ?? '')) &&
          e.closest('li')?.textContent?.includes(within)
      )
      if (!el) return ['not on the page']
      const out: string[] = []
      const r = el.getBoundingClientRect()
      if (el.scrollWidth > el.clientWidth + 1) out.push(`${el.scrollWidth}px of text in ${el.clientWidth}px`)
      if (r.left < 0 || r.right > document.documentElement.clientWidth + 0.5) out.push(`box ${r.left}..${r.right} outside the viewport`)
      for (let a = el.parentElement; a && a !== document.body; a = a.parentElement) {
        const s = getComputedStyle(a)
        if (s.overflowX === 'visible' && s.overflowY === 'visible') continue
        const b = a.getBoundingClientRect()
        if (r.left < b.left - 0.5 || r.right > b.right + 0.5 || r.bottom > b.bottom + 0.5) out.push(`clipped by <${a.tagName.toLowerCase()}> ${b.left}..${b.right}`)
      }
      return out
    },
    { source: text.source, flags: text.flags, within }
  )
}

/**
 * What of an open dialog does not fit the viewport: the dialog's own box past
 * an edge, or content wider than it. A dialog is fixed, so the page's own
 * scroll width never shows it.
 */
async function dialogOverflow(page: Page): Promise<string[]> {
  return page.evaluate(() => {
    const out: string[] = []
    const vw = document.documentElement.clientWidth
    for (const d of Array.from(document.querySelectorAll<HTMLElement>('[role="dialog"], [role="alertdialog"]'))) {
      const r = d.getBoundingClientRect()
      const name = d.getAttribute('aria-label') ?? d.querySelector('h2')?.textContent ?? 'dialog'
      if (r.left < -0.5 || r.right > vw + 0.5) out.push(`"${name}": ${r.left}..${r.right} in ${vw}px`)
      if (d.scrollWidth > d.clientWidth + 1) out.push(`"${name}": ${d.scrollWidth}px of content in ${d.clientWidth}px`)
    }
    return out
  })
}

/** A week ending today, as the YYYY-MM-DD range a share is made over. */
function lastWeek(): { from: string; to: string } {
  const day = (d: Date) => d.toISOString().slice(0, 10)
  const now = new Date()
  return { from: day(new Date(now.getTime() - 6 * 86_400_000)), to: day(now) }
}

test.use({ viewport: PHONE })

test('no page scrolls sideways on a phone', async ({ page, request }) => {
  test.setTimeout(180_000)
  // A long name and origins that do not fit a phone's width on one line,
  // as real projects have: they wrap or truncate, never widen the page.
  const created = await request.post('/api/projects', {
    headers: authHeaders(),
    data: {
      name: `phone-${Date.now()} - longer than a phone is wide on one line`,
      allowed_origins: ['https://www.a-long-subdomain.example.com', 'https://billing-test.another-long-subdomain.example.com'],
    },
  })
  expect(created.ok(), await created.text()).toBeTruthy()
  const { project_id: id, key } = (await created.json()) as { project_id: number; key: string }
  // A draft form with a name and values longer than a phone is wide: its
  // row, its page and its table wrap or scroll inside their card.
  const formName = 'a-contact-form-whose-name-is-longer-than-a-phone-is-wide-on-one'
  await submitJSON(request, key, formName, {
    email: 'someone.with.a.long.address@a-long-subdomain.example.com',
    message: 'A message that goes on and on, longer than a phone is wide, as people write them',
    a_field_with_a_long_name_that_does_not_fit: 'x',
  })

  // Product events with names longer than a phone is wide, one renamed and
  // one blocked: the Settings page's Events table and its alias chips wrap.
  const longEvent = 'an_event_whose_name_is_longer_than_a_phone_is_wide_on_one_line'
  const renamedEvent = 'the_new_name_of_that_event_which_is_longer_than_a_phone_is_wide'
  const blockedEvent = 'another_event_that_is_blocked_with_a_name_longer_than_a_phone'
  // The events carry attribute keys longer than a phone is wide too, one
  // renamed and one blocked: the Attributes section's rows and chips wrap.
  const longAttribute = 'an_attribute_key_longer_than_a_phone_is_wide_on_one_line'
  const renamedAttribute = 'the_new_name_of_that_attribute_longer_than_a_phone_is_wide'
  const blockedAttribute = 'another_attribute_that_is_blocked_with_a_key_longer_than_a_phone'
  const ingested = await request.post('/ingest/events', {
    headers: { 'X-Analytics-Key': key },
    data: {
      events: [longEvent, blockedEvent].map((name) => ({
        id: crypto.randomUUID(),
        ts: new Date().toISOString(),
        name,
        family: 'product',
        attributes: { [longAttribute]: 'a-value', [blockedAttribute]: 'another-value' },
      })),
    },
  })
  expect(ingested.status(), await ingested.text()).toBe(202)
  // The write buffer flushes every few seconds; rules apply to what is stored.
  const today = new Date().toISOString().slice(0, 10)
  await expect
    .poll(async () => {
      const res = await request.get(`/api/projects/${id}/event-names?from=${today}&to=${today}`, { headers: authHeaders() })
      return ((await res.json()) as { events: unknown[] }).events.length
    }, { timeout: 30_000 })
    .toBe(2)
  await attributesListed(request, id, [longAttribute, blockedAttribute])
  for (const [kind, action, body] of [
    ['event', 'rename', { family: 'product', from: longEvent, to: renamedEvent }],
    ['event', 'block', { family: 'product', name: blockedEvent }],
    ['attribute', 'rename', { from: longAttribute, to: renamedAttribute }],
    ['attribute', 'block', { key: blockedAttribute }],
  ] as const) {
    const rule = await request.post(`/api/projects/${id}/${kind}-rules/${action}`, { headers: authHeaders(), data: body })
    expect(rule.ok(), await rule.text()).toBeTruthy()
  }

  // Two shares of one widget with a 120-character title, over the long-named
  // project: the Shares page and, once one is archived, the Archive page show them.
  const dashboard = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: {
      title: 'Phone shares',
      range: '7d',
      widgets: [{ component: 'stat', title: 'short', source: { type: 'query', content: JSON.stringify({ query: { kind: 'stat', table: 'views', metrics: [{ op: 'count' }] }, rename: { count: 'value' } }) }, width: 6, height: 4 }],
    },
  })
  expect(dashboard.ok(), await dashboard.text()).toBeTruthy()
  const { dashboard_id: dashboardId } = (await dashboard.json()) as { dashboard_id: number }
  const dashboardBody = await request.get(`/api/dashboards/${dashboardId}`, { headers: authHeaders() })
  const widgetId = ((await dashboardBody.json()) as { widgets: { widget_id: number }[] }).widgets[0].widget_id
  const stamp = String(Date.now())
  const title = `${stamp} ${'a long widget title that goes on and on '.repeat(4)}`.slice(0, 120).replace(/ $/, 'x')
  const renamed = await request.patch(`/api/widgets/${widgetId}`, { headers: authHeaders(), data: { title } })
  expect(renamed.ok(), await renamed.text()).toBeTruthy()
  const shares = [
    await createShare(request, { widgetId, projectId: id, ...lastWeek() }),
    await createShare(request, { widgetId, projectId: id, ...lastWeek() }),
  ]

  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  expect(await sidewaysScroll(page), 'login').toEqual([])
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)

  // A dashboard of our own whose title is longer than a phone is wide, as a
  // tab of that project: the tab select fits the phone however long its value.
  const long = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: { title: 'A dashboard of our own whose title is longer than a phone is wide', range: '7d' },
  })
  expect(long.ok(), await long.text()).toBeTruthy()
  const { dashboard_id: longId } = (await long.json()) as { dashboard_id: number }
  const tab = await request.post(`/api/projects/${id}/tabs`, { headers: authHeaders(), data: { dashboard_id: longId } })
  expect(tab.ok(), await tab.text()).toBeTruthy()

  for (const path of [
    '/app/projects',
    '/app/projects/1/settings',
    `/app/projects/${id}/settings`,
    // Today, where the events above were received (the default range ends yesterday).
    `/app/projects/${id}/settings?range=today`,
    `/app/projects/${id}/forms`,
    `/app/projects/${id}/forms/${formName}`,
    `/app/projects/${id}/dashboards/1`,
    `/app/projects/${id}/dashboards/${longId}`,
    '/app/archive',
    '/app/gallery/components',
    '/app/gallery/dashboards',
    // The not-found pages: an unknown address, dashboard, project and project tab.
    '/app/nowhere',
    '/app/dashboards/999999',
    '/app/projects/999999/settings',
    `/app/projects/${id}/dashboards/999999`,
  ]) {
    await check(page, path)
    // The check above means nothing for the Events and Attributes sections unless they are drawn.
    if (path.endsWith('?range=today')) {
      for (const [section, alias] of [['Events', longEvent], ['Attributes', longAttribute]]) {
        const region = page.getByRole('region', { name: section })
        await expect(region.getByRole('button', { name: `Remove alias ${alias}` })).toBeVisible()
        await expect(region.getByText('Blocked', { exact: true })).toBeVisible()
      }
    }
  }
  for (const d of await dashboardIds(request)) await check(page, `/app/dashboards/${d}`)

  // The Attributes section's dialogs, over its long keys, fit the phone.
  await check(page, `/app/projects/${id}/settings?range=today`)
  const attributes = page.getByRole('region', { name: 'Attributes' })
  await attributes.getByRole('button', { name: `Actions for ${renamedAttribute}` }).click()
  await page.getByRole('menuitem', { name: 'Merge into…' }).click()
  await expect(page.getByRole('dialog', { name: `Merge ${renamedAttribute} into…` })).toBeVisible()
  await expect.poll(() => dialogOverflow(page), { message: 'merge attribute dialog' }).toEqual([])
  expect(await sidewaysScroll(page), 'merge attribute dialog').toEqual([])
  await page.keyboard.press('Escape')
  await attributes.getByRole('button', { name: `Actions for ${renamedAttribute}` }).click()
  await page.getByRole('menuitem', { name: 'Block…' }).click()
  await expect(page.getByRole('alertdialog', { name: `Block ${renamedAttribute}?` })).toBeVisible()
  await expect.poll(() => dialogOverflow(page), { message: 'block attribute dialog' }).toEqual([])
  expect(await sidewaysScroll(page), 'block attribute dialog').toEqual([])
  await page.keyboard.press('Escape')

  // A form's Approve, Settings and Stop accepting dialogs and a submission's drawer fit the phone.
  await check(page, `/app/projects/${id}/forms/${formName}`)
  await page.getByRole('button', { name: 'Approve', exact: true }).click()
  await expect(page.getByRole('dialog', { name: `Approve ${formName}` })).toBeVisible()
  // Polled: the dialog zooms in, and its box fits once it has.
  await expect.poll(() => dialogOverflow(page), { message: 'Approve dialog' }).toEqual([])
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: `Actions for ${formName}` }).click()
  await page.getByRole('menuitem', { name: 'Settings…' }).click()
  await expect(page.getByRole('dialog', { name: 'Settings' })).toBeVisible()
  await expect.poll(() => dialogOverflow(page), { message: 'Settings dialog' }).toEqual([])
  expect(await sidewaysScroll(page), 'Settings dialog').toEqual([])
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: `Actions for ${formName}` }).click()
  await page.getByRole('menuitem', { name: 'Stop accepting…' }).click()
  await expect(page.getByRole('dialog', { name: `Stop accepting ${formName}` })).toBeVisible()
  await expect.poll(() => dialogOverflow(page), { message: 'Stop accepting dialog' }).toEqual([])
  await page.keyboard.press('Escape')
  await page.locator('tbody tr').first().click()
  await expect(page.getByRole('dialog', { name: 'Submission' }).getByText('x', { exact: true })).toBeVisible()
  // Polled: the drawer slides in from the right edge, and its box fits once it has.
  await expect.poll(() => dialogOverflow(page), { message: 'submission drawer' }).toEqual([])
  expect(await sidewaysScroll(page), 'submission drawer').toEqual([])
  await page.keyboard.press('Escape')
  const archivedDash = await request.post(`/api/dashboards/${longId}/archive`, { headers: authHeaders(), data: {} })
  expect(archivedDash.ok(), await archivedDash.text()).toBeTruthy()

  // The Shares page, with the long-title share's row on it: at the phone and,
  // since the table folds below xl, beside the 256px sidebar at 1024 and 1280 too.
  for (const width of [PHONE.width, 1024, 1280]) {
    await page.setViewportSize({ width, height: PHONE.height })
    await check(page, '/app/shares')
    await expect(page.locator(`a[href="${shares[0].url}"]`), `share row at ${width}px`).toBeVisible()
    await expect(page.getByRole('row').filter({ has: page.locator(`a[href="${shares[0].url}"]`) })).toContainText(stamp)
  }
  await page.setViewportSize(PHONE)

  // The public share page and the server's 404 pages (a dead share, any
  // other address), in both colour schemes.
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme })
    for (const url of [shares[0].url, '/share/00000000-0000-7000-8000-000000000000', '/nowhere']) {
      await page.goto(url)
      await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
      await page.waitForLoadState('networkidle')
      expect(await sidewaysScroll(page), `${url} (${colorScheme})`).toEqual([])
    }
  }
  await page.emulateMedia({ colorScheme: null })

  // An archived share: its "archived · deleted on <date>" line shows whole, and nothing scrolls.
  const archivedShare = await request.post(`/api/widget-shares/${shares[1].id}/archive`, { headers: authHeaders() })
  expect(archivedShare.ok(), await archivedShare.text()).toBeTruthy()
  await check(page, '/app/archive')
  const archivedRow = page.getByRole('listitem').filter({ hasText: stamp })
  await expect(archivedRow.getByText(/^archived · deleted on /)).toBeVisible()
  expect(await clipped(page, /^archived · deleted on /, stamp), 'archived share status').toEqual([])

  // Its Restore dialog fits the phone.
  await archivedRow.getByRole('button', { name: 'Restore' }).click()
  const restore = page.getByRole('dialog', { name: 'Restore share' })
  await expect(restore.getByLabel('Archive after')).toBeVisible()
  expect(await sidewaysScroll(page), 'Restore share dialog').toEqual([])
  expect(await dialogOverflow(page), 'Restore share dialog').toEqual([])
  await page.keyboard.press('Escape')
  await expect(restore).toHaveCount(0)

  // The Share dialog, from the first widget of dashboard 1: with its preview,
  // then with the link it makes. With no clipboard, so the link and embed
  // code show as text, its widest state.
  await page.goto('/app/dashboards/1')
  await page.waitForLoadState('networkidle')
  const card = page.locator('[data-slot="widget-card"]').first()
  await card.hover()
  await card.getByRole('button', { name: 'Widget actions' }).click()
  await expect(page.getByRole('menuitem', { name: 'Share…' })).toBeEnabled()
  await page.getByRole('menuitem', { name: 'Share…' }).click()
  const shareDialog = page.getByRole('dialog', { name: 'Share widget' })
  await expect(shareDialog.getByRole('img', { name: 'Preview of the share card' })).toBeVisible({ timeout: 30_000 })
  expect(await sidewaysScroll(page), 'Share dialog').toEqual([])
  expect(await dialogOverflow(page), 'Share dialog').toEqual([])
  await page.evaluate(() => Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true }))
  await shareDialog.getByRole('button', { name: 'Create and copy link' }).click()
  await expect(shareDialog.getByRole('textbox', { name: 'Embed code' })).toBeVisible()
  expect(await sidewaysScroll(page), 'Share dialog, link made').toEqual([])
  expect(await dialogOverflow(page), 'Share dialog, link made').toEqual([])
  await page.keyboard.press('Escape')
  await expect(shareDialog).toHaveCount(0)

  // A user dashboard whose one widget follows the project and the range: its
  // header holds Add widget beside both switchers, and they fit the phone.
  // Its table answers five columns with names far longer than a phone is
  // wide, over the seeded project's views: in the editor, after a run, that
  // table scrolls inside its own box and nothing else scrolls sideways.
  const longName = (name: string) => `${name}_with_a_name_longer_than_a_phone_is_wide_on_one_line`
  const wide = {
    query: {
      kind: 'breakdown',
      table: 'views',
      metrics: [
        { op: 'count', as: longName('views') },
        { op: 'actors', as: longName('actors') },
      ],
      dimensions: ['path', 'referrer_source', 'browser'],
      limit: 3,
    },
    rename: { path: longName('path'), referrer_source: longName('referrer_source'), browser: longName('browser') },
  }
  const projects = await request.get('/api/projects', { headers: authHeaders() })
  const dev = ((await projects.json()) as { projects: { project_id: number; name: string }[] }).projects.find((x) => x.name === 'dev')
  expect(dev, 'no seeded dev project').toBeDefined()
  const editable = await request.post('/api/dashboards', {
    headers: authHeaders(),
    data: {
      title: 'Phone editor',
      range: '7d',
      widgets: [
        {
          component: 'table',
          title: 'Five long columns',
          source: { type: 'query', content: JSON.stringify(wide) },
          width: 12,
          height: 8,
        },
      ],
    },
  })
  expect(editable.ok(), await editable.text()).toBeTruthy()
  const { dashboard_id: editableId } = (await editable.json()) as { dashboard_id: number }
  await check(page, `/app/dashboards/${editableId}?project=${dev!.project_id}`)
  await expect(page.getByRole('button', { name: 'Add widget' })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Project: / })).toBeVisible()
  await expect(page.getByRole('button', { name: /^Range: / })).toBeVisible()
  const wideCard = page.locator('[data-slot="widget-card"]', { hasText: 'Five long columns' })
  await wideCard.hover()
  await wideCard.getByRole('button', { name: 'Widget actions' }).click()
  await page.getByRole('menuitem', { name: 'Edit…' }).click()
  const editor = page.getByRole('dialog', { name: 'Edit widget' })
  await expect(editor.getByRole('combobox', { name: 'Project' })).toBeVisible()
  await expect(editor.getByRole('button', { name: /^Range: / })).toBeVisible()
  // The edit runs its query as it opens.
  await expect(editor.getByText(/^\d+ rows? · \d+ ms/)).toBeVisible()
  // The preview draws the table in a widget card; the rows below the query are drawn bare.
  for (const part of ['Preview', 'Rows']) {
    const results = editor.getByRole('region', { name: part }).locator('[data-slot="table-container"]')
    await expect(results).toBeVisible()
    // The rows are wider than the phone: they scroll, inside their own box.
    expect(await results.evaluate((el) => el.scrollWidth > el.clientWidth), `${part} rows wider than their box`).toBe(true)
  }
  const own = '[role="dialog"] section[aria-label="Rows"] [data-slot="table-container"]'
  await expect.poll(() => dialogOverflow(page), { message: 'editor' }).toEqual([])
  expect(await sidewaysScroll(page, own), 'editor').toEqual([])
  await page.keyboard.press('Escape')
  await expect(editor).toHaveCount(0)

  // The archived project, on the Archive page: its long name stays on one line.
  const archived = await request.post(`/api/projects/${id}/archive`, { headers: authHeaders() })
  expect(archived.ok(), await archived.text()).toBeTruthy()
  await check(page, '/app/archive')
  await expect(page.getByRole('region', { name: 'Projects' }).getByRole('link', { name: /^phone-/ })).toBeVisible()
  expect(await sidewaysScroll(page), 'archived projects').toEqual([])
})
