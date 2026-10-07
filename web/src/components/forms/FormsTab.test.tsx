import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { endpoints, type Project } from '@/lib/api'
import { formatDay } from '@/lib/forms'
import { draft, form, fromNow } from '@/test/forms'
import { renderWithProviders } from '@/test/render'
import FormsTab from './FormsTab'

const project: Project = { project_id: 4, name: 'shop', allowed_origins: ['https://shop.example'] }

beforeEach(() => {
  vi.restoreAllMocks()
  vi.spyOn(endpoints, 'keys').mockResolvedValue({ keys: [{ project_id: 4, label: 'web', key: 'ak_web_123', state: 'active' }] })
})

function renderTab() {
  return renderWithProviders(
    <MemoryRouter initialEntries={['/projects/4/forms?range=7d']}>
      <FormsTab project={project} />
    </MemoryRouter>
  )
}

describe('FormsTab', () => {
  it('lists the forms in the order the API gives, drafts first, each with its status', async () => {
    const closes = fromNow(3)
    vi.spyOn(endpoints, 'forms').mockResolvedValue({
      forms: [
        draft('signup', 2.5, { purpose: 'Beta list', submissions: 3, last_submitted_at: fromNow(-1) }),
        draft('late', 5 / 24),
        form('contact', { purpose: 'Sales', submissions: 12 }),
        form('event', { closes_at: closes }),
        form('survey', { closes_at: fromNow(-1) }),
      ],
    })
    renderTab()
    const list = await screen.findByRole('list', { name: 'Forms' })
    const rows = within(list).getAllByRole('listitem')
    expect(rows.map((r) => within(r).getByRole('link').textContent)).toEqual([
      expect.stringContaining('signup'),
      expect.stringContaining('late'),
      expect.stringContaining('contact'),
      expect.stringContaining('event'),
      expect.stringContaining('survey'),
    ])
    expect(within(rows[0]).getByText('Draft · expires in 3 days')).toBeInTheDocument()
    expect(within(rows[0]).getByText('Beta list')).toBeInTheDocument()
    expect(within(rows[0]).getByText(/3 submissions/)).toBeInTheDocument()
    expect(within(rows[1]).getByText('Draft · expires today')).toBeInTheDocument()
    expect(within(rows[2]).getByText('Approved')).toBeInTheDocument()
    expect(within(rows[2]).getByText(/12 submissions/)).toBeInTheDocument()
    expect(within(rows[3]).getByText(`Closes ${formatDay(new Date(closes))}`)).toBeInTheDocument()
    expect(within(rows[4]).getByText('Closed')).toBeInTheDocument()
  })

  it("opens a form's page from its row, keeping the range", async () => {
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ forms: [form('contact')] })
    renderTab()
    const link = await screen.findByRole('link', { name: /contact/ })
    expect(link).toHaveAttribute('href', '/projects/4/forms/contact?range=7d')
  })

  it('stops a form now and archives one from its menu', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ forms: [form('contact')] })
    const update = vi.spyOn(endpoints, 'updateForm').mockResolvedValue({ status: 'updated' })
    const archive = vi.spyOn(endpoints, 'archiveForm').mockResolvedValue({ status: 'archived' })
    renderTab()
    await user.click(await screen.findByRole('button', { name: 'Actions for contact' }))
    const before = Date.now()
    await user.click(await screen.findByRole('menuitem', { name: 'Stop now' }))
    await waitFor(() => expect(update).toHaveBeenCalled())
    const [pid, name, body] = update.mock.calls[0]
    expect([pid, name]).toEqual([4, 'contact'])
    expect(body.closes_at).toMatch(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\dZ$/)
    expect(Date.parse(body.closes_at as string)).toBeGreaterThanOrEqual(Math.floor(before / 1000) * 1000)

    await user.click(screen.getByRole('button', { name: 'Actions for contact' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Archive' }))
    await waitFor(() => expect(archive).toHaveBeenCalledWith(4, 'contact'))
  })

  it('approves a draft from its menu with the fields picked', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ forms: [draft('signup', 3, { fields: ['email', 'name'] })] })
    const approve = vi.spyOn(endpoints, 'approveForm').mockResolvedValue({ status: 'approved' })
    renderTab()
    await user.click(await screen.findByRole('button', { name: 'Actions for signup' }))
    await user.click(await screen.findByRole('menuitem', { name: 'Approve…' }))
    const dialog = await screen.findByRole('dialog', { name: 'Approve signup' })
    await user.click(within(dialog).getByRole('checkbox', { name: 'name' }))
    await user.click(within(dialog).getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(approve).toHaveBeenCalledWith(4, 'signup', ['email']))
  })

  it('offers no Approve… on an approved form, and no Stop now on a closed one', async () => {
    const user = userEvent.setup()
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ forms: [form('survey', { closes_at: fromNow(-1) })] })
    renderTab()
    await user.click(await screen.findByRole('button', { name: 'Actions for survey' }))
    await screen.findByRole('menuitem', { name: 'Archive' })
    expect(screen.queryByRole('menuitem', { name: 'Approve…' })).toBeNull()
    expect(screen.queryByRole('menuitem', { name: 'Stop now' })).toBeNull()
  })

  it('shows a hint with a copyable form snippet and its action URL when there are no forms', async () => {
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ forms: [] })
    renderTab()
    expect(await screen.findByText(/No forms yet/)).toBeInTheDocument()
    expect(screen.getByText(/<form data-twillingate-form="contact">/)).toBeInTheDocument()
    expect(await screen.findByText(/\/ingest\/forms\/contact\?key=ak_web_123/)).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /^Copy/ }).length).toBeGreaterThanOrEqual(2)
  })

  it('heads the tab with Find a person', async () => {
    vi.spyOn(endpoints, 'forms').mockResolvedValue({ forms: [form('contact')] })
    renderTab()
    expect(await screen.findByRole('searchbox', { name: 'Find a person' })).toBeInTheDocument()
  })
})
