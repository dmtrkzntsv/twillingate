import { readFileSync } from 'node:fs'
import { expect, test, type APIRequestContext, type Page } from '@playwright/test'
import { createProject } from './forms'

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
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/forms$`))
  const row = page.getByRole('list', { name: 'Forms' }).getByRole('listitem').filter({ hasText: 'contact' })
  await expect(row.getByText(/^Draft · expires in \d+ days?$/)).toBeVisible()
  await expect(row.getByText(/^2 submissions/)).toBeVisible()

  // The draft's page: its banner, and every field it saw as a column.
  await row.getByRole('link').click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/forms/contact$`))
  // The Forms button leads the page; the form's own "Forms" back link follows it.
  await expect(page.getByRole('link', { name: /^Forms/ }).first()).toHaveAttribute('aria-current', 'page')
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
  const fields = page.getByRole('group', { name: 'Expected fields' })
  await expect(fields.getByRole('checkbox', { name: 'email' })).toBeChecked()
  await expect(fields.getByText('not kept')).toHaveCount(2)
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
  await table.getByRole('button', { name: 'CSV', exact: true }).click()
  const file = await download
  expect(file.suggestedFilename()).toBe('contact-submissions.csv')
  const text = readFileSync((await file.path())!, 'utf8')
  expect(text).toContain('ann@example.com')
  expect(text).not.toContain('bob@example.com')

  // Stop now: the form closes and refuses the next submission.
  await page.getByRole('button', { name: 'Stop now' }).click()
  await expect(page.getByRole('group', { name: 'Closing' }).getByText(/^Closed /)).toBeVisible()
  const refused = await request.post(`/ingest/forms/contact?key=${key}`, {
    headers: { Origin: SITE, Referer: `${SITE}/contact` },
    form: { email: 'late@example.com' },
    maxRedirects: 0,
  })
  expect(refused.status()).toBe(303)
  expect(refused.headers()['location']).toBe(`${SITE}/contact#twillingate-form-error-contact`)
  await expect(page.getByRole('group', { name: 'Closing' }).getByRole('button', { name: 'Reopen' })).toBeVisible()

  // Find a person across the forms, and delete everything found.
  await page.getByRole('link', { name: /^Forms(, \d+ new)?$/ }).last().click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/forms$`))
  await page.getByRole('searchbox', { name: 'Find a person' }).fill('bob@')
  await page.getByRole('button', { name: 'Find', exact: true }).click()
  const found = page.getByRole('region', { name: 'Found submissions' })
  await expect(found.getByText(/^1 submission contains/)).toBeVisible()
  await expect(found.getByRole('heading', { name: 'contact' })).toBeVisible()
  await found.getByRole('button', { name: 'Delete all' }).click()
  await page.getByRole('alertdialog').getByRole('button', { name: 'Delete submissions' }).click()
  await expect(page.getByText('Deleted 1 submission')).toBeVisible()
  await expect(row.getByText(/^1 submission\b/)).toBeVisible()
})

test('the Forms button counts new submissions until a form is opened', async ({ page, request }) => {
  test.setTimeout(120_000)
  const { id, key } = await createProject(request, `forms-count-${Date.now()}`, [SITE])
  await postForm(request, key, { email: 'ann@example.com', message: 'Hello' })

  await login(page)
  await page.goto(`/app/projects/${id}/settings`)
  const forms = page.getByRole('link', { name: /^Forms/ })
  await expect(forms).toHaveAccessibleName('Forms, 1 new')

  await forms.click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/forms$`))
  await page.getByRole('list', { name: 'Forms' }).getByRole('listitem').filter({ hasText: 'contact' }).getByRole('link').click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/forms/contact$`))
  await expect(page.getByRole('region', { name: 'Submissions' })).toBeVisible()

  // Opening the form marked it seen: back on the project, the count is gone.
  await page.getByRole('link', { name: 'Settings', exact: true }).click()
  await expect(page).toHaveURL(new RegExp(`/app/projects/${id}/settings$`))
  await expect(page.getByRole('link', { name: 'Forms', exact: true })).toBeVisible()
})
