// The submissions table's view (filters and sort) as FormPage keeps and
// sends it. The table is a stub that sets a view, so these tests read what
// the page does with one rather than how the table's filter editor works
// (widgets/table.test.tsx has that).
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type DashboardsResponse, type Project } from '@/lib/api'
import type { TableView } from '@/lib/table-view'
import { contactPage, form, submission } from '@/test/forms'
import { renderWithProviders } from '@/test/render'
import FormPage from './FormPage'

vi.mock('@/components/widgets/table', () => ({
  default: ({
    view,
    onView,
    selection,
  }: {
    view: TableView
    onView: (v: TableView) => void
    selection?: { onChange: (next: Set<number>) => void }
  }) => (
    <div role="table" aria-label="Stub table">
      <button type="button" onClick={() => selection?.onChange(new Set([0]))}>
        Check the first row
      </button>
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
  vi.spyOn(endpoints, 'forms').mockResolvedValue({
    action_base: '',
    forms: [form('contact', { submissions: 2, archived_submissions: 1 })],
  })
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ dashboards: [], purge_after_days: 30 } as unknown as DashboardsResponse)
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

  it('archives the rows checked, then clears the selection', async () => {
    const user = userEvent.setup()
    const archive = vi.spyOn(endpoints, 'archiveSubmissions').mockResolvedValue({ archived: 1 })
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Check the first row' }))
    const bar = screen.getByRole('toolbar', { name: 'Selected submissions' })
    expect(within(bar).getByText('1 selected')).toBeInTheDocument()
    // The selection's Export takes the place of the whole table's.
    expect(screen.queryByRole('button', { name: 'Export CSV' })).toBeNull()
    await user.click(within(bar).getByRole('button', { name: 'Archive' }))
    await waitFor(() => expect(archive).toHaveBeenCalledWith(4, { ids: ['s1'] }))
    await waitFor(() => expect(screen.queryByRole('toolbar', { name: 'Selected submissions' })).toBeNull())
  })

  it('clears the selection on Clear', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Check the first row' }))
    await user.click(screen.getByRole('button', { name: 'Clear' }))
    expect(screen.queryByRole('toolbar', { name: 'Selected submissions' })).toBeNull()
  })

  it('switches to the archived submissions, a table of their own behind the archived link, and restores the rows checked', async () => {
    const user = userEvent.setup()
    const restore = vi.spyOn(endpoints, 'restoreSubmissions').mockResolvedValue({ restored: 1 })
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Filter Bob' }))
    await waitFor(() => expect(endpoints.submissions).toHaveBeenCalledWith(4, 'contact', { filters: JSON.stringify(bob) }))
    await user.click(screen.getByRole('button', { name: '1 archived' }))
    expect(await screen.findByRole('heading', { name: /^Archived/ })).toBeInTheDocument()
    // A fresh view: the active table's filter stays with it.
    await waitFor(() => expect(endpoints.submissions).toHaveBeenLastCalledWith(4, 'contact', { archived: true }))
    expect(screen.getByText(/deleted for good 30 days after archiving/)).toBeInTheDocument()
    vi.mocked(endpoints.submissions).mockResolvedValue(bobPage)
    await user.click(await screen.findByRole('button', { name: 'Filter Bob' }))
    await waitFor(() =>
      expect(endpoints.submissions).toHaveBeenCalledWith(4, 'contact', { archived: true, filters: JSON.stringify(bob) })
    )
    await waitFor(() => expect(screen.queryByText('ann@example.com')).toBeNull())
    await user.click(screen.getByRole('button', { name: 'Check the first row' }))
    const bar = screen.getByRole('toolbar', { name: 'Selected submissions' })
    await user.click(within(bar).getByRole('button', { name: 'Restore' }))
    await waitFor(() => expect(restore).toHaveBeenCalledWith(4, { ids: ['s2'] }))
    await user.click(screen.getByRole('button', { name: 'Submissions' }))
    expect(await screen.findByRole('heading', { name: /^Submissions/ })).toBeInTheDocument()
  })

  it('exports the rows checked from the page, without asking the server', async () => {
    const user = userEvent.setup()
    const csv = vi.spyOn(endpoints, 'exportSubmissions')
    let blob: Blob | undefined
    URL.createObjectURL = vi.fn((b: Blob) => {
      blob = b
      return 'blob:csv'
    }) as typeof URL.createObjectURL
    URL.revokeObjectURL = vi.fn()
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Check the first row' }))
    await user.click(within(screen.getByRole('toolbar', { name: 'Selected submissions' })).getByRole('button', { name: 'Export' }))
    await waitFor(() => expect(blob).toBeDefined())
    expect(await blob!.text()).toBe('Received,email,message\r\n2026-10-05T10:00:00Z,ann@example.com,Hello\r\n')
    expect(csv).not.toHaveBeenCalled()
  })

  it('downloads the CSV of what the filters match', async () => {
    const user = userEvent.setup()
    const csv = vi.spyOn(endpoints, 'exportSubmissions').mockResolvedValue(new Blob(['Received,email\n'], { type: 'text/csv' }))
    URL.createObjectURL = vi.fn(() => 'blob:csv')
    URL.revokeObjectURL = vi.fn()
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    renderPage()
    await user.click(await screen.findByRole('button', { name: 'Filter Bob' }))
    await user.click(screen.getByRole('button', { name: 'Export CSV' }))
    await waitFor(() => expect(csv).toHaveBeenCalledWith(4, 'contact', { filters: JSON.stringify(bob) }))
    await waitFor(() => expect(click).toHaveBeenCalled())
  })
})
