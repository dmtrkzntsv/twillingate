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
  it('links back to the forms, keeping the range', async () => {
    renderPage()
    expect(await screen.findByRole('link', { name: /Forms/ })).toHaveAttribute('href', '/projects/4/forms?range=7d')
  })

  it('builds the submissions table from the page the API answers', async () => {
    renderPage()
    const table = await screen.findByRole('table')
    const headers = within(table).getAllByRole('columnheader').map((h) => h.textContent)
    expect(headers).toEqual(contactPage.columns)
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

  it('keeps Delete all matching off without filters', async () => {
    renderPage()
    await screen.findByRole('table')
    expect(screen.getByRole('button', { name: 'Delete all matching' })).toBeDisabled()
  })

  it('marks fields arriving outside the expected list as not kept, and saves a new list', async () => {
    const user = userEvent.setup()
    const update = vi.spyOn(endpoints, 'updateForm').mockResolvedValue({ status: 'updated' })
    renderPage()
    const picker = await screen.findByRole('group', { name: 'Expected fields' })
    expect(within(picker).getByText('not kept')).toBeInTheDocument()
    expect(within(picker).getByRole('button', { name: 'Save fields' })).toBeDisabled()
    await user.click(within(picker).getByRole('checkbox', { name: 'phone' }))
    await user.click(within(picker).getByRole('button', { name: 'Save fields' }))
    await waitFor(() => expect(update).toHaveBeenCalledWith(4, 'contact', { expected_fields: ['email', 'message', 'phone'] }))
  })

  it('saves the purpose and return URL', async () => {
    const user = userEvent.setup()
    const update = vi.spyOn(endpoints, 'updateForm').mockResolvedValue({ status: 'updated' })
    renderPage()
    const purpose = await screen.findByLabelText('Purpose')
    expect(purpose).toHaveValue('Sales')
    await user.clear(purpose)
    await user.type(purpose, 'Leads')
    await user.type(screen.getByLabelText('Return URL'), 'https://shop.example/thanks')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(update).toHaveBeenCalledWith(4, 'contact', { purpose: 'Leads', return_url: 'https://shop.example/thanks' })
    )
  })

  it('shows a draft banner with Approve', async () => {
    const user = userEvent.setup()
    const until = new Date(Date.now() + 3 * 86_400_000).toISOString()
    vi.mocked(endpoints.forms).mockResolvedValue({ action_base: '', forms: [draft('contact', 3, { draft_until: until, fields: ['email', 'message'] })] })
    const approve = vi.spyOn(endpoints, 'approveForm').mockResolvedValue({ status: 'approved' })
    renderPage()
    expect(await screen.findByText(`Draft: accepting until ${formatDay(new Date(until))}, then archived`)).toBeInTheDocument()
    expect(screen.queryByRole('group', { name: 'Expected fields' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Approve' }))
    const dialog = await screen.findByRole('dialog', { name: 'Approve contact' })
    await user.click(within(dialog).getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(approve).toHaveBeenCalledWith(4, 'contact', ['email', 'message']))
  })

  it('closes the form from its settings', async () => {
    const user = userEvent.setup()
    const update = vi.spyOn(endpoints, 'updateForm').mockResolvedValue({ status: 'updated' })
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Stop now' }))
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
