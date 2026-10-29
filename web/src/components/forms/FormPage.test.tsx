import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type Project } from '@/lib/api'
import { formatDay } from '@/lib/forms'
import { contactPage, draft, form, submission } from '@/test/forms'
import { renderWithProviders } from '@/test/render'
import FormPage from './FormPage'

const project: Project = { project_id: 4, name: 'shop', allowed_origins: ['https://shop.example'] }

beforeEach(() => {
  vi.restoreAllMocks()
  localStorage.clear()
  vi.spyOn(endpoints, 'forms').mockResolvedValue({
    action_base: '',
    forms: [form('contact', { fields: ['email', 'message', 'phone'], expected_fields: ['email', 'message'], submissions: 2, purpose: 'Sales' })],
  })
  vi.spyOn(endpoints, 'submissions').mockResolvedValue(contactPage)
  vi.spyOn(endpoints, 'submission').mockResolvedValue(submission('s2'))
})

function renderPage(name = 'contact') {
  return renderWithProviders(
    <MemoryRouter initialEntries={[`/projects/4/forms/${name}?range=7d`]}>
      <FormPage project={project} name={name} />
    </MemoryRouter>
  )
}

describe('FormPage', () => {
  it('marks a form with new submissions seen up to its last one, once', async () => {
    const last = '2026-10-05T10:00:00Z'
    vi.spyOn(endpoints, 'forms').mockResolvedValue({
      action_base: '',
      forms: [form('contact', { submissions: 2, new_submissions: 2, last_submitted_at: last })],
    })
    const seen = vi.spyOn(endpoints, 'markFormSeen').mockResolvedValue({ status: 'seen' })
    const { client } = renderPage()
    await screen.findByRole('table')
    await waitFor(() => expect(seen).toHaveBeenCalledWith(4, 'contact', last))
    // The refetch that follows still says 2 new (a stub): the same `until` is not sent again.
    await waitFor(() => expect(client.isFetching({ queryKey: ['forms', 4] })).toBe(0))
    expect(seen).toHaveBeenCalledTimes(1)
  })

  it('refreshes the forms and the activity counts after marking seen', async () => {
    vi.spyOn(endpoints, 'forms').mockResolvedValue({
      action_base: '',
      forms: [form('contact', { submissions: 2, new_submissions: 2, last_submitted_at: '2026-10-05T10:00:00Z' })],
    })
    vi.spyOn(endpoints, 'markFormSeen').mockResolvedValue({ status: 'seen' })
    const { client } = renderPage()
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: ['project-activity'] }))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['forms', 4] })
  })

  it('marks nothing seen when no submission is new', async () => {
    const seen = vi.spyOn(endpoints, 'markFormSeen').mockResolvedValue({ status: 'seen' })
    renderPage()
    await screen.findByRole('table')
    expect(seen).not.toHaveBeenCalled()
  })

  it('links back to the forms, keeping the range', async () => {
    renderPage()
    expect(await screen.findByRole('link', { name: /Forms/ })).toHaveAttribute('href', '/projects/4/forms?range=7d')
  })

  it('builds the submissions table from the page the API answers', async () => {
    renderPage()
    const table = await screen.findByRole('table')
    const headers = within(table).getAllByRole('columnheader').map((h) => h.textContent)
    // The first header holds the select-all box.
    expect(headers).toEqual(['', ...contactPage.columns])
    expect(within(table).getByText('ann@example.com')).toBeInTheDocument()
    expect(within(table).getByText('Hi there')).toBeInTheDocument()
    expect(endpoints.submissions).toHaveBeenCalledWith(4, 'contact', {})
  })

  it("opens a row's submission in the drawer by its id", async () => {
    const user = userEvent.setup()
    renderPage()
    const row = await screen.findByRole('button', { name: /bob@example\.com/ })
    await user.click(row)
    const drawer = await screen.findByRole('dialog', { name: 'Submission' })
    expect(endpoints.submission).toHaveBeenCalledWith(4, 'contact', 's2')
    expect(await within(drawer).findByText('555-0100')).toBeInTheDocument()
  })

  it('checks rows with a box each, and every row of the page from the header', async () => {
    const user = userEvent.setup()
    renderPage()
    const table = await screen.findByRole('table')
    await user.click(within(table).getByRole('checkbox', { name: 'Select row 2' }))
    expect(screen.getByText('1 selected')).toBeInTheDocument()
    // Checking a row never opens its drawer.
    expect(screen.queryByRole('dialog', { name: 'Submission' })).toBeNull()
    await user.click(within(table).getByRole('checkbox', { name: 'Select all rows' }))
    expect(screen.getByText('2 selected')).toBeInTheDocument()
    await user.click(within(table).getByRole('checkbox', { name: 'Select all rows' }))
    expect(screen.queryByText(/selected$/)).toBeNull()
  })

  async function openSettings(user: ReturnType<typeof userEvent.setup>) {
    await user.click(await screen.findByRole('button', { name: 'Actions for contact' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Settings…' }))
    return screen.findByRole('dialog', { name: 'Settings' })
  }

  it('marks fields arriving outside the expected list as not kept, and saves a new list', async () => {
    const user = userEvent.setup()
    const update = vi.spyOn(endpoints, 'updateForm').mockResolvedValue({ status: 'updated' })
    renderPage()
    const dialog = await openSettings(user)
    const picker = within(dialog).getByRole('group', { name: 'Expected fields' })
    expect(within(picker).getByText('not kept')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Save' })).toBeDisabled()
    await user.click(within(picker).getByRole('checkbox', { name: 'phone' }))
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(update).toHaveBeenCalledWith(4, 'contact', { expected_fields: ['email', 'message', 'phone'] }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Settings' })).toBeNull())
  })

  it('adds a field by name and keeps it with the others', async () => {
    const user = userEvent.setup()
    const update = vi.spyOn(endpoints, 'updateForm').mockResolvedValue({ status: 'updated' })
    renderPage()
    const dialog = await openSettings(user)
    const picker = within(dialog).getByRole('group', { name: 'Expected fields' })
    await user.type(within(picker).getByRole('textbox', { name: 'New field' }), 'company')
    await user.click(within(picker).getByRole('button', { name: 'Add field' }))
    expect(within(picker).getByRole('checkbox', { name: 'company' })).toBeChecked()
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(update).toHaveBeenCalledWith(4, 'contact', { expected_fields: ['email', 'message', 'company'] }))
  })

  it('saves the purpose, return URL and fields together, only what changed', async () => {
    const user = userEvent.setup()
    const update = vi.spyOn(endpoints, 'updateForm').mockResolvedValue({ status: 'updated' })
    renderPage()
    const dialog = await openSettings(user)
    const purpose = within(dialog).getByLabelText('Purpose')
    expect(purpose).toHaveValue('Sales')
    await user.clear(purpose)
    await user.type(purpose, 'Leads')
    await user.type(within(dialog).getByLabelText('Return URL'), 'https://shop.example/thanks')
    await user.click(within(dialog).getByRole('checkbox', { name: 'phone' }))
    await user.click(within(dialog).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(update).toHaveBeenCalledWith(4, 'contact', {
        purpose: 'Leads',
        return_url: 'https://shop.example/thanks',
        expected_fields: ['email', 'message', 'phone'],
      })
    )
  })

  it('drops unsaved edits on Cancel', async () => {
    const user = userEvent.setup()
    const update = vi.spyOn(endpoints, 'updateForm')
    renderPage()
    let dialog = await openSettings(user)
    await user.clear(within(dialog).getByLabelText('Purpose'))
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Settings' })).toBeNull())
    dialog = await openSettings(user)
    expect(within(dialog).getByLabelText('Purpose')).toHaveValue('Sales')
    expect(update).not.toHaveBeenCalled()
  })

  it('shows a draft banner with Approve', async () => {
    const user = userEvent.setup()
    const until = new Date(Date.now() + 3 * 86_400_000).toISOString()
    vi.mocked(endpoints.forms).mockResolvedValue({ action_base: '', forms: [draft('contact', 3, { draft_until: until, fields: ['email', 'message'] })] })
    const approve = vi.spyOn(endpoints, 'approveForm').mockResolvedValue({ status: 'approved' })
    renderPage()
    expect(await screen.findByText(`Draft: accepting until ${formatDay(new Date(until))}, then archived`)).toBeInTheDocument()
    const settings = await openSettings(user)
    expect(within(settings).getByLabelText('Purpose')).toBeInTheDocument()
    expect(within(settings).queryByRole('group', { name: 'Expected fields' })).toBeNull()
    await user.click(within(settings).getByRole('button', { name: 'Cancel' }))
    await user.click(screen.getByRole('button', { name: 'Approve' }))
    const dialog = await screen.findByRole('dialog', { name: 'Approve contact' })
    await user.click(within(dialog).getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(approve).toHaveBeenCalledWith(4, 'contact', ['email', 'message']))
  })

  it('stops the form from its menu', async () => {
    const user = userEvent.setup()
    const update = vi.spyOn(endpoints, 'updateForm').mockResolvedValue({ status: 'updated' })
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Actions for contact' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Stop accepting…' }))
    const dialog = await screen.findByRole('dialog', { name: 'Stop accepting contact' })
    await user.click(within(dialog).getByRole('button', { name: 'Stop now' }))
    await waitFor(() => expect(update).toHaveBeenCalledWith(4, 'contact', { closes_at: expect.stringMatching(/Z$/) }))
  })

  it('says when the form is not among the active ones', async () => {
    vi.mocked(endpoints.forms).mockResolvedValue({ action_base: '', forms: [] })
    renderPage('gone')
    expect(await screen.findByText(/No form gone/)).toBeInTheDocument()
  })

  it('says when a form has no submissions yet', async () => {
    vi.mocked(endpoints.submissions).mockResolvedValue({ ...contactPage, rows: [], ids: [], matched: 0, total: 0 })
    renderPage()
    expect(await screen.findByText(/No submissions yet/)).toBeInTheDocument()
  })
})
