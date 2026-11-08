import { readFileSync } from 'node:fs'
import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { createProject, submitJSON } from './forms'

// Matches web/e2e/serve.sh's CONSOLE_AUTH_DSN (token://e2e-token?password=e2e-pass&...).
const PASSWORD = 'e2e-pass'
const SITE = 'https://forms-e2e.example'

async function login(page: Page): Promise<void> {
  await page.goto('/app/')
  await page.waitForURL(/\/oauth\/authorize\?/)
  await page.getByLabel('Password').fill(PASSWORD)
  await page.getByRole('button', { name: 'Connect' }).click()
  await page.waitForURL(/\/app\/projects$/)
}

/**
 * Posts a plain HTML form from the site's contact page, as a browser does:
 * urlencoded, with the page's Origin and Referer. The collector answers with
 * a redirect back to the page's success fragment, which is not followed.
 */
async function postForm(request: APIRequestContext, key: string, fields: Record<string, string>): Promise<void> {
  await expect(async () => {
    const res = await request.post(`/ingest/forms/contact?key=${key}`, {
      headers: { Origin: SITE, Referer: `${SITE}/contact` },
      form: fields,
      maxRedirects: 0,
    })
    expect(res.status(), await res.text()).toBe(303)
    expect(res.headers()['location']).toBe(`${SITE}/contact#twillingate-form-success-contact`)
  }).toPass({ timeout: 15_000 })
}

test('a form arrives as a draft, is approved, filtered, closed, exported, and a person erased', async ({ page, request }) => {
  test.setTimeout(120_000)
  const { id, key } = await createProject(request, `forms-${Date.now()}`, [SITE])
  await postForm(request, key, { email: 'ann@example.com', message: 'Hello', phone: '555-0100' })
  await postForm(request, key, { email: 'bob@example.com', message: 'Hi there' })

  await login(page)
  await page.goto(`/app/projects/${id}/settings`)
  await page.getByRole('link', { name: /^Forms/ }).click()
  // Forms lands on the project's one form, a tab, with a draft's mark.
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/forms/contact$`))
  const tabs = page.getByRole('tablist', { name: 'Tabs' }).getByRole('tab')
  await expect(tabs).toHaveCount(1)
  await expect(tabs.first()).toHaveAccessibleName(/^contact \(draft/)
  await expect(tabs.first()).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByRole('link', { name: /^Back to the dashboards of forms-/ })).toBeVisible()
  await expect(page.getByText(/^Draft · expires in \d+ days?$/)).toBeVisible()

  // The draft's page: its banner, and every field it saw as a column.
  await expect(page.getByRole('link', { name: /^Forms/ })).toHaveAttribute('aria-current', 'page')
  await expect(page.getByText(/^Draft: accepting until .+, then archived$/)).toBeVisible()
  const table = page.getByRole('region', { name: 'Submissions' })
  await expect(table.getByRole('columnheader', { name: 'phone' })).toBeVisible()

  // Approve with email alone.
  await page.getByRole('button', { name: 'Approve', exact: true }).click()
  const approve = page.getByRole('dialog', { name: 'Approve contact' })
  await approve.getByRole('checkbox', { name: 'message' }).click()
  await approve.getByRole('checkbox', { name: 'phone' }).click()
  await approve.getByRole('button', { name: 'Approve', exact: true }).click()
  await expect(approve).toHaveCount(0)
  await expect(page.getByText(/^Draft: accepting until/)).toHaveCount(0)
  await page.getByRole('button', { name: 'Actions for contact' }).click()
  await page.getByRole('menuitem', { name: 'Settings…' }).click()
  const settings = page.getByRole('dialog', { name: 'Settings' })
  const fields = settings.getByRole('group', { name: 'Expected fields' })
  await expect(fields.getByRole('checkbox', { name: 'email' })).toBeChecked()
  await expect(fields.getByText('not kept')).toHaveCount(2)
  await page.keyboard.press('Escape')
  await expect(settings).toHaveCount(0)
  await expect(table.getByRole('columnheader', { name: 'phone' })).toHaveCount(0)
  await expect(table.locator('tbody tr')).toHaveCount(2)

  // Filter the table on the server: one row left.
  await table.getByRole('button', { name: 'Filter', exact: true }).click()
  await page.getByRole('combobox', { name: 'Column' }).click()
  await page.getByRole('option', { name: 'email', exact: true }).click()
  await page.getByRole('option', { name: /ann@example\.com/ }).click()
  await page.getByRole('button', { name: 'Apply', exact: true }).click()
  await expect(table.getByRole('button', { name: 'email in ann@example.com', exact: true })).toBeVisible()
  await expect(table.locator('tbody tr')).toHaveCount(1)
  await expect(table.locator('tbody tr')).toContainText('ann@example.com')

  // A row opens its drawer, with every stored field.
  await table.locator('tbody tr').first().click()
  const drawer = page.getByRole('dialog', { name: 'Submission' })
  await expect(drawer.getByText('555-0100')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(drawer).toHaveCount(0)

  // The CSV of what the filter matches downloads.
  const download = page.waitForEvent('download')
  await table.getByRole('button', { name: 'Export CSV', exact: true }).click()
  const file = await download
  expect(file.suggestedFilename()).toBe('contact-submissions.csv')
  const text = readFileSync((await file.path())!, 'utf8')
  expect(text).toContain('ann@example.com')
  expect(text).not.toContain('bob@example.com')

  // A checked row exports alone, from the page.
  await table.getByRole('checkbox', { name: 'Select row 1' }).click()
  const bar = table.getByRole('toolbar', { name: 'Selected submissions' })
  await expect(bar.getByText('1 selected')).toBeVisible()
  const picked = page.waitForEvent('download')
  await bar.getByRole('button', { name: 'Export', exact: true }).click()
  expect((await picked).suggestedFilename()).toBe('contact-selected-submissions.csv')
  await bar.getByRole('button', { name: 'Clear', exact: true }).click()
  await expect(bar).toHaveCount(0)

  // Stop accepting, now: the form closes and refuses the next submission.
  await page.getByRole('button', { name: 'Actions for contact' }).click()
  await page.getByRole('menuitem', { name: 'Stop accepting…' }).click()
  const stop = page.getByRole('dialog', { name: 'Stop accepting contact' })
  await stop.getByRole('button', { name: 'Stop now' }).click()
  await expect(stop).toHaveCount(0)
  await expect(page.getByText('Closed', { exact: true })).toBeVisible()
  const refused = await request.post(`/ingest/forms/contact?key=${key}`, {
    headers: { Origin: SITE, Referer: `${SITE}/contact` },
    form: { email: 'late@example.com' },
    maxRedirects: 0,
  })
  expect(refused.status()).toBe(303)
  expect(refused.headers()['location']).toBe(`${SITE}/contact#twillingate-form-error-contact`)
  await page.getByRole('button', { name: 'Actions for contact' }).click()
  await expect(page.getByRole('menuitem', { name: 'Reopen' })).toBeVisible()
  await expect(page.getByRole('menuitem', { name: 'Stop accepting…' })).toHaveCount(0)
  await page.keyboard.press('Escape')

  // Find a person across the forms, from the tab row, and archive everything found.
  await page.getByRole('button', { name: 'Find a person' }).click()
  const finder = page.getByRole('dialog', { name: 'Find a person' })
  await finder.getByRole('searchbox', { name: 'Find a person' }).fill('bob@')
  await finder.getByRole('button', { name: 'Find', exact: true }).click()
  const found = finder.getByRole('region', { name: 'Found submissions' })
  await expect(found.getByText(/^1 submission contains/)).toBeVisible()
  await expect(found.getByRole('heading', { name: 'contact' })).toBeVisible()
  await found.getByRole('button', { name: 'Archive all' }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Archive submissions' }).click()
  await expect(page.getByText('Archived 1 submission')).toBeVisible()
  // The confirm closes first: while it is open the finder is hidden from the tree, not gone.
  await expect(page.getByRole('alertdialog')).toHaveCount(0)
  await expect(finder.getByRole('searchbox', { name: 'Find a person' })).toHaveValue('')
  await page.keyboard.press('Escape')
  await expect(finder).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Submissions 1' })).toBeVisible()

  // The archived submission waits behind the form's archived link, and comes back.
  await page.getByRole('button', { name: '1 archived', exact: true }).click()
  await page.getByRole('button', { name: /bob@example\.com/ }).click()
  await page.getByRole('dialog', { name: 'Submission' }).getByRole('button', { name: 'Restore' }).click()
  await expect(page.getByText('Restored 1 submission')).toBeVisible()
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: 'Submissions', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Submissions 2' })).toBeVisible()
})

test('the Forms button counts new submissions until a form is opened, then dots the draft', async ({ page, request }) => {
  test.setTimeout(120_000)
  const { id, key } = await createProject(request, `forms-count-${Date.now()}`, [SITE])
  await postForm(request, key, { email: 'ann@example.com', message: 'Hello' })

  await login(page)
  await page.goto(`/app/projects/${id}/settings`)
  const forms = page.getByRole('link', { name: /^Forms/ })
  // The first submission made the form a draft.
  await expect(forms).toHaveAccessibleName('Forms, 1 new, 1 draft')

  await forms.click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/forms/contact$`))
  await expect(page.getByRole('region', { name: 'Submissions' })).toBeVisible()

  // Opening the form marked it seen: back on the project, the count is
  // gone and the draft, still waiting to be approved, keeps a dot.
  await page.getByRole('link', { name: 'Settings', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/settings$`))
  // The URL moves before the form page is swapped out (navigations render
  // in a transition), and that page has a second link named Forms.
  await expect(page.getByRole('region', { name: 'Submissions' })).toHaveCount(0)
  await expect(page.getByRole('link', { name: 'Forms, 1 draft', exact: true })).toBeVisible()
})

test('a form is created from the console, opens as a tab with its setup, and its tab is dragged first', async ({ page, request }) => {
  test.setTimeout(120_000)
  const { id, key } = await createProject(request, `forms-new-${Date.now()}`, [SITE])
  // A draft first, so the new form comes second.
  await submitJSON(request, key, 'contact', { email: 'ann@example.com' })

  await login(page)
  await page.goto(`/app/projects/${id}/forms`)
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/forms/contact$`))
  const tabs = page.getByRole('tablist', { name: 'Tabs' }).getByRole('tab')
  await expect(tabs).toHaveCount(1)

  // New form, from "+": the name is checked as it is typed.
  await page.getByRole('button', { name: 'New form' }).click()
  const dialog = page.getByRole('dialog', { name: 'New form' })
  await dialog.getByLabel('Name').fill('Sign Up')
  await expect(dialog.getByText('Use 1 to 64 lower-case letters, digits, _ and -.')).toBeVisible()
  await dialog.getByLabel('Name').fill('signup')
  await dialog.getByLabel('Purpose').fill('Beta list')
  await dialog.getByRole('textbox', { name: 'New field' }).fill('email')
  await dialog.getByRole('button', { name: 'Add field' }).click()
  await dialog.getByRole('textbox', { name: 'New field' }).fill('company')
  await dialog.getByRole('button', { name: 'Add field' }).click()
  await dialog.getByLabel('Return address').fill(`${SITE}/thanks`)
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(dialog).toHaveCount(0)

  // Its tab, last; approved, with no submissions yet, so Setup is open on its own snippet.
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/forms/signup$`))
  await expect(tabs).toHaveCount(2)
  await expect(tabs.nth(1)).toHaveAccessibleName('signup')
  await expect(tabs.nth(1)).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByText('Approved', { exact: true })).toBeVisible()
  const setup = page.getByRole('region', { name: 'Setup' })
  await expect(setup.getByRole('button', { name: 'Setup' })).toHaveAttribute('aria-expanded', 'true')
  await expect(setup.getByText(/<form data-twillingate-form="signup">/)).toContainText('<input name="company">')
  await expect(setup.getByText(new RegExp(`/ingest/forms/signup\\?key=${key}$`))).toBeVisible()

  // A name the project has is said on the name.
  await page.getByRole('button', { name: 'New form' }).click()
  await dialog.getByLabel('Name').fill('contact')
  await dialog.getByRole('textbox', { name: 'New field' }).fill('email')
  await dialog.getByRole('button', { name: 'Add field' }).click()
  await dialog.getByRole('button', { name: 'Create' }).click()
  await expect(dialog.getByText('A form named contact already exists (it may be archived).')).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(dialog).toHaveCount(0)

  // Drag signup before contact: the order is the project's, after a reload too.
  const from = await tabs.nth(1).boundingBox()
  const to = await tabs.first().boundingBox()
  if (!from || !to) throw new Error('a tab has no bounding box')
  // Raw mouse moves: dnd-kit reads pointer events, and the first move must clear its activation distance.
  await page.mouse.move(from.x + from.width / 2, from.y + from.height / 2)
  const moved = page.waitForResponse((r) => /\/forms\/signup\/move$/.test(r.url()) && r.request().method() === 'POST')
  await page.mouse.down()
  await page.mouse.move(from.x + from.width / 2 - 15, from.y + from.height / 2, { steps: 5 })
  await page.mouse.move(to.x + 4, to.y + to.height / 2, { steps: 10 })
  await page.mouse.up()
  expect((await moved).ok()).toBeTruthy()
  await expect(tabs.first()).toHaveAccessibleName('signup')
  await page.reload()
  await expect(tabs.first()).toHaveAccessibleName('signup')
  await expect(tabs.nth(1)).toHaveAccessibleName(/^contact \(draft/)

  // /forms lands back on the form last opened.
  await tabs.nth(1).click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/forms/contact$`))
  await expect(page.getByRole('heading', { name: 'contact', level: 2 })).toBeVisible()
  await page.goto(`/app/projects/${id}/forms`)
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/forms/contact$`))
})
