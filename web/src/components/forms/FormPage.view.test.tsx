// The submissions table's view (filters and sort) as FormPage keeps and
// sends it. The table is a stub that sets a view, so these tests read what
// the page does with one rather than how the table's filter editor works
// (widgets/table.test.tsx has that).
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type Project } from '@/lib/api'
import type { TableView } from '@/lib/table-view'
import { contactPage, form, submission } from '@/test/forms'
import { renderWithProviders } from '@/test/render'
import FormPage from './FormPage'

vi.mock('@/components/widgets/table', () => ({
  default: ({ view, onView }: { view: TableView; onView: (v: TableView) => void }) => (
    <div role="table" aria-label="Stub table">
      <button type="button" onClick={() => onView({ ...view, filters: [{ column: 'email', op: '=', value: 'bob@example.com' }], offset: 0 })}>
        Filter Bob
      </button>
      <button type="button" onClick={() => onView({ ...view, sort: { column: 'Received', dir: 'asc' }, offset: 0 })}>
        Oldest first
      </button>
    </div>
  ),
}))

const project: Project = { project_id: 4, name: 'shop', allowed_origins: ['https://shop.example'] }
const KEY = 'twillingate:forms:4:contact'
const bob = [{ column: 'email', op: '=', value: 'bob@example.com' }]
const bobPage = { ...contactPage, rows: [contactPage.rows[1]], ids: ['s2'], matched: 1 }

beforeEach(() => {
  vi.restoreAllMocks()
  localStorage.clear()
  vi.spyOn(endpoints, 'forms').mockResolvedValue({ action_base: '', forms: [form('contact', { submissions: 2 })] })
  vi.spyOn(endpoints, 'submissions').mockResolvedValue(contactPage)
  vi.spyOn(endpoints, 'submission').mockResolvedValue(submission('s2'))
})

function renderPage() {
  return renderWithProviders(
    <MemoryRouter initialEntries={['/projects/4/forms/contact']}>
      <FormPage project={project} name="contact" />
    </MemoryRouter>
  )
}

describe('FormPage view', () => {
  it('keeps only the sort in this browser, never a filter value', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Filter Bob' }))
    await user.click(screen.getByRole('button', { name: 'Oldest first' }))
    await waitFor(() =>
      expect(endpoints.submissions).toHaveBeenCalledWith(4, 'contact', { filters: JSON.stringify(bob), sort: 'Received:asc' })
    )
    expect(JSON.parse(localStorage.getItem(KEY) ?? 'null')).toEqual({ sort: { column: 'Received', dir: 'asc' } })
    for (let i = 0; i < localStorage.length; i++) {
      expect(localStorage.getItem(localStorage.key(i)!)).not.toContain('bob@example.com')
    }
  })

  it('reads back the stored sort but no stored filters', async () => {
    localStorage.setItem(KEY, JSON.stringify({ filters: bob, sort: { column: 'Received', dir: 'asc' } }))
    renderPage()
    await waitFor(() => expect(endpoints.submissions).toHaveBeenCalledWith(4, 'contact', { sort: 'Received:asc' }))
    expect(endpoints.submissions).not.toHaveBeenCalledWith(4, 'contact', expect.objectContaining({ filters: expect.anything() }))
  })

  it('deletes what the filters match once confirmed', async () => {
    const user = userEvent.setup()
    const del = vi.spyOn(endpoints, 'deleteSubmissions').mockResolvedValue({ deleted: 1 })
    renderPage()
    vi.mocked(endpoints.submissions).mockResolvedValue(bobPage)
    await user.click(await screen.findByRole('button', { name: 'Filter Bob' }))
    await waitFor(() => expect(endpoints.submissions).toHaveBeenCalledWith(4, 'contact', { filters: JSON.stringify(bob) }))
    const button = screen.getByRole('button', { name: 'Delete all matching' })
    await waitFor(() => expect(button).toBeEnabled())
    await user.click(button)
    const confirm = await screen.findByRole('alertdialog', { name: 'Delete 1 matching submission?' })
    await user.click(within(confirm).getByRole('button', { name: 'Delete submissions' }))
    await waitFor(() => expect(del).toHaveBeenCalledWith(4, { form: 'contact', filters: JSON.stringify(bob) }))
  })

  it('keeps Delete all matching off while a new view loads, so the count and the filters sent agree', async () => {
    const user = userEvent.setup()
    let resolve: (p: typeof contactPage) => void = () => {}
    renderPage()
    await screen.findByRole('button', { name: 'Filter Bob' })
    vi.mocked(endpoints.submissions)
      .mockResolvedValueOnce(bobPage)
      .mockImplementationOnce(() => new Promise((r) => (resolve = r)))
    await user.click(screen.getByRole('button', { name: 'Filter Bob' }))
    const button = screen.getByRole('button', { name: 'Delete all matching' })
    await waitFor(() => expect(button).toBeEnabled())
    // A new sort asks again: until it answers, the rows and count on screen are the old view's.
    await user.click(screen.getByRole('button', { name: 'Oldest first' }))
    await waitFor(() => expect(endpoints.submissions).toHaveBeenCalledTimes(3))
    expect(button).toBeDisabled()
    resolve(bobPage)
    await waitFor(() => expect(button).toBeEnabled())
  })

  it('downloads the CSV of what the filters match', async () => {
    const user = userEvent.setup()
    const csv = vi.spyOn(endpoints, 'exportSubmissions').mockResolvedValue(new Blob(['Received,email\n'], { type: 'text/csv' }))
    URL.createObjectURL = vi.fn(() => 'blob:csv')
    URL.revokeObjectURL = vi.fn()
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Filter Bob' }))
    await user.click(screen.getByRole('button', { name: 'CSV' }))
    await waitFor(() => expect(csv).toHaveBeenCalledWith(4, 'contact', { filters: JSON.stringify(bob) }))
    await waitFor(() => expect(click).toHaveBeenCalled())
  })
})
