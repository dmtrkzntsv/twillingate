import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router'
import { toast } from 'sonner'
import { ApiError, endpoints, type Alert, type AlertPatch, type Project } from '@/lib/api'
import { alert, serveAlerts } from '@/test/alerts'
import { renderWithProviders } from '@/test/render'
import AlertPage from './AlertPage'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))

const project: Project = { project_id: 1, name: 'shop', allowed_origins: [] }

beforeEach(() => {
  vi.restoreAllMocks()
  vi.mocked(toast).mockClear()
  vi.mocked(toast.success).mockClear()
  vi.mocked(toast.error).mockClear()
  vi.spyOn(endpoints, 'previewAlert').mockResolvedValue({ days: [], gaps: [], fired: 0 })
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC' } as Awaited<ReturnType<typeof endpoints.dashboards>>)
  vi.spyOn(endpoints, 'eventNames').mockResolvedValue({
    events: [
      { family: 'product', name: 'signup', events: 40 },
      { family: 'product', name: 'paid', events: 10 },
    ],
  })
})

function renderPage(alertId = '7') {
  return renderWithProviders(
    <MemoryRouter initialEntries={[`/projects/1/alerts/${alertId}`]}>
      <AlertPage project={project} alertId={alertId} />
    </MemoryRouter>
  )
}

async function form() {
  return within(await screen.findByRole('form', { name: 'Alert settings' }))
}

describe('AlertEditor', () => {
  it('offers a silence alert only its hours: no condition, window or minimum volume', async () => {
    serveAlerts([alert({ id: 7, kind: 'silence', unit: 'hours', condition: { type: 'silence', hours: 6 }, where: [] })])
    renderPage()
    const f = await form()
    expect(f.getByLabelText('Name')).toHaveValue('Signups')
    expect(f.getByLabelText('Hours without an event')).toHaveValue('6')
    expect(f.queryByLabelText('Condition')).toBeNull()
    expect(f.queryByLabelText('Window')).toBeNull()
    expect(f.queryByLabelText('Minimum volume')).toBeNull()
  })

  it('hides the minimum volume of a stat count threshold, and shows it for a change', async () => {
    serveAlerts([alert({ id: 7 })])
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    expect(f.getByLabelText('Condition')).toHaveValue('threshold')
    expect(f.getByLabelText('Window')).toHaveValue('1')
    expect(f.queryByLabelText('Minimum volume')).toBeNull()
    await user.selectOptions(f.getByLabelText('Condition'), 'change')
    expect(f.getByLabelText('Minimum volume')).toBeInTheDocument()
    expect(f.getByLabelText('Percent')).toHaveValue('30')
  })

  it('keeps Save disabled until a change, and Discard puts the saved values back', async () => {
    serveAlerts([alert({ id: 7 })])
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    expect(f.getByRole('button', { name: 'Save' })).toBeDisabled()
    await user.clear(f.getByLabelText('Threshold'))
    await user.type(f.getByLabelText('Threshold'), '40')
    expect(f.getByRole('button', { name: 'Save' })).toBeEnabled()
    await user.click(f.getByRole('button', { name: 'Discard' }))
    expect(f.getByLabelText('Threshold')).toHaveValue('50')
    expect(f.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('sends one save while one is on its way, however often the form is submitted', async () => {
    serveAlerts([alert({ id: 7 })])
    const update = vi.spyOn(endpoints, 'updateAlert').mockReturnValue(new Promise(() => {}))
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    await user.clear(f.getByLabelText('Threshold'))
    await user.type(f.getByLabelText('Threshold'), '40')
    const settings = screen.getByRole('form', { name: 'Alert settings' })
    fireEvent.submit(settings)
    await vi.waitFor(() => expect(update).toHaveBeenCalledTimes(1))
    fireEvent.submit(settings)
    fireEvent.submit(settings)
    await new Promise((r) => setTimeout(r, 20))
    expect(update).toHaveBeenCalledTimes(1)
  })

  it('reads the saved backtest once after a save, of the new definition', async () => {
    const a = alert({ id: 7 })
    const { lists } = serveAlerts([a])
    const preview = vi.mocked(endpoints.previewAlert)
    const saved: Alert = { ...a, condition: { type: 'threshold', op: 'below', value: 40 }, updated_at: '2026-11-26T09:00:00Z' }
    vi.spyOn(endpoints, 'updateAlert').mockImplementation(async () => {
      lists.live = [saved]
      return saved
    })
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    await vi.waitFor(() => expect(preview).toHaveBeenCalledWith(1, { id: 7 }))
    await user.clear(f.getByLabelText('Threshold'))
    await user.type(f.getByLabelText('Threshold'), '40')
    await user.click(f.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(toast.success).toHaveBeenCalledWith('Saved'))
    await new Promise((r) => setTimeout(r, 50))
    expect(preview.mock.calls.filter(([, body]) => 'id' in body)).toHaveLength(2)
  })

  it('reads the saved backtest no more after a rename', async () => {
    const a = alert({ id: 7 })
    const { lists } = serveAlerts([a])
    const preview = vi.mocked(endpoints.previewAlert)
    const saved: Alert = { ...a, name: 'Sign-ups', updated_at: '2026-11-26T09:00:00Z' }
    vi.spyOn(endpoints, 'updateAlert').mockImplementation(async () => {
      lists.live = [saved]
      return saved
    })
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    await vi.waitFor(() => expect(preview).toHaveBeenCalledWith(1, { id: 7 }))
    await user.clear(f.getByLabelText('Name'))
    await user.type(f.getByLabelText('Name'), 'Sign-ups')
    await user.click(f.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(toast.success).toHaveBeenCalledWith('Saved'))
    await new Promise((r) => setTimeout(r, 50))
    expect(preview.mock.calls.filter(([, body]) => 'id' in body)).toHaveLength(1)
  })

  it('reads the saved backtest again after a save that kept updated_at (two edits within a second)', async () => {
    const a = alert({ id: 7 })
    const { lists } = serveAlerts([a])
    const preview = vi.mocked(endpoints.previewAlert)
    const saved: Alert = { ...a, condition: { type: 'threshold', op: 'below', value: 40 } }
    vi.spyOn(endpoints, 'updateAlert').mockImplementation(async () => {
      lists.live = [saved]
      return saved
    })
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    await vi.waitFor(() => expect(preview).toHaveBeenCalledWith(1, { id: 7 }))
    await user.clear(f.getByLabelText('Threshold'))
    await user.type(f.getByLabelText('Threshold'), '40')
    await user.click(f.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(preview.mock.calls.filter(([, body]) => 'id' in body)).toHaveLength(2))
  })

  it('shows an error under its field and keeps Save disabled', async () => {
    serveAlerts([alert({ id: 7 })])
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    await user.clear(f.getByLabelText('Window'))
    await user.type(f.getByLabelText('Window'), '31')
    expect(f.getByText('Enter whole days from 1 to 30')).toBeInTheDocument()
    expect(f.getByLabelText('Window')).toHaveAttribute('aria-invalid', 'true')
    expect(f.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it("saves a fraction threshold's untouched value as it was, sending only what changed", async () => {
    const a = alert({ id: 7, unit: 'fraction', read: { metric: 'rate', volume: 'count' }, condition: { type: 'threshold', op: 'below', value: 0.03 }, window_days: 7 })
    const { lists } = serveAlerts([a])
    const update = vi.spyOn(endpoints, 'updateAlert').mockImplementation(async (_p: number, _id: number, patch: AlertPatch) => {
      const next = alert({ ...a, ...patch } as Partial<Alert>)
      lists.live = [next]
      return next
    })
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    expect(f.getByLabelText('Threshold')).toHaveValue('3')
    await user.clear(f.getByLabelText('Window'))
    await user.type(f.getByLabelText('Window'), '14')
    await user.click(f.getByRole('button', { name: 'Save' }))
    expect(update).toHaveBeenCalledWith(1, 7, { window_days: 14 })
    expect(toast.success).toHaveBeenCalledWith('Saved')
    expect(f.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('shows what was never sent, from the answer to a save', async () => {
    const a = alert({ id: 7 })
    serveAlerts([a])
    vi.spyOn(endpoints, 'updateAlert').mockResolvedValue(
      alert({ ...a, condition: { type: 'threshold', op: 'below', value: 40 }, warnings: ['`signup` has never been sent by this project; the alert reads no_data until it is'] })
    )
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    await user.clear(f.getByLabelText('Threshold'))
    await user.type(f.getByLabelText('Threshold'), '40')
    await user.click(f.getByRole('button', { name: 'Save' }))
    expect(await f.findByText('Never sent yet: signup')).toBeInTheDocument()
  })

  it("shows a 409's message under the form and reads the alerts again, keeping the edit over what another saved", async () => {
    const { spy, lists } = serveAlerts([alert({ id: 7 })])
    vi.spyOn(endpoints, 'updateAlert').mockRejectedValue(new ApiError(409, 'a live alert is already named Paid'))
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    await user.clear(f.getByLabelText('Name'))
    await user.type(f.getByLabelText('Name'), 'Paid')
    const reads = spy.mock.calls.length
    // Another edit landed meanwhile: a week's window.
    lists.live = [alert({ id: 7, window_days: 7 })]
    await user.click(f.getByRole('button', { name: 'Save' }))
    expect(await f.findByText('a live alert is already named Paid')).toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalled()
    expect(spy.mock.calls.length).toBeGreaterThan(reads)
    // The edit stays, to try again; the input left alone shows the other edit.
    expect(f.getByLabelText('Name')).toHaveValue('Paid')
    await vi.waitFor(() => expect(f.getByLabelText('Window')).toHaveValue('7'))
  })

  it('shows only the name of a broken alert', async () => {
    serveAlerts([alert({ id: 7, kind: '', unit: '', query: null, read: {}, condition: { type: '' } as unknown as Alert['condition'], window_days: 0, state: 'broken' })])
    renderPage()
    const f = await form()
    expect(f.getByLabelText('Name')).toBeInTheDocument()
    expect(f.queryByLabelText('Condition')).toBeNull()
    expect(f.queryByLabelText('Threshold')).toBeNull()
    expect(f.queryByLabelText('Window')).toBeNull()
  })

  it('has no editor for an archived alert, which reads its settings instead', async () => {
    serveAlerts([], [alert({ id: 7, archived_at: '2026-11-20T10:00:00Z' })])
    renderPage()
    await screen.findByRole('heading', { level: 2, name: 'Signups' })
    expect(screen.queryByRole('form', { name: 'Alert settings' })).toBeNull()
    expect(screen.getByText('below 50 over 1 day')).toBeInTheDocument()
  })
})

describe('TemplateFields', () => {
  it('keeps a saved event the event list lacks', async () => {
    serveAlerts([alert({ id: 7, template: 'event_count', fields: { event: 'legacy_signup' } })])
    renderPage()
    const f = await form()
    const event = f.getByLabelText('Event')
    expect(event).toHaveValue('legacy_signup')
    await vi.waitFor(() => expect(within(event).getByRole('option', { name: 'paid' })).toBeInTheDocument())
    expect(event).toHaveValue('legacy_signup')
    expect(f.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('offers a path only for $page_view, and drops it when the event changes', async () => {
    const a = alert({ id: 7, template: 'event_count', fields: { event: 'signup' } })
    serveAlerts([a])
    const update = vi.spyOn(endpoints, 'updateAlert').mockResolvedValue(a)
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    expect(f.queryByLabelText('Page path')).toBeNull()
    await user.selectOptions(f.getByLabelText('Event'), '$page_view')
    await user.type(f.getByLabelText('Page path'), '/pricing')
    await user.selectOptions(f.getByLabelText('Event'), 'paid')
    expect(f.queryByLabelText('Page path')).toBeNull()
    await user.click(f.getByRole('button', { name: 'Save' }))
    expect(update).toHaveBeenCalledWith(1, 7, { fields: { event: 'paid' } })
  })

  it("offers unique_people's every event and its people", async () => {
    serveAlerts([alert({ id: 7, template: 'unique_people', fields: { who: 'users' } })])
    renderPage()
    const f = await form()
    expect(f.getByLabelText('Event')).toHaveValue('')
    expect(within(f.getByLabelText('Event')).getByRole('option', { name: 'Every event' })).toBeInTheDocument()
    expect(f.getByLabelText('Who')).toHaveValue('users')
    expect(within(f.getByLabelText('Who')).getByRole('option', { name: 'Signed-in users' })).toBeInTheDocument()
  })

  it("offers conversion's two events and its people", async () => {
    serveAlerts([alert({ id: 7, kind: 'funnel', unit: 'fraction', template: 'conversion', fields: { from_event: '$page_view', to_event: 'signup' } })])
    renderPage()
    const f = await form()
    expect(f.getByLabelText('From event')).toHaveValue('$page_view')
    expect(f.getByLabelText('From page path')).toBeInTheDocument()
    expect(f.getByLabelText('To event')).toHaveValue('signup')
    expect(f.getByLabelText('To page path')).toHaveValue('')
    expect(f.getByLabelText('People')).toHaveValue('actor')
  })

  it("offers a conversion step's path on any event, and keeps it when the event changes", async () => {
    const a = alert({
      id: 7,
      kind: 'funnel',
      unit: 'fraction',
      template: 'conversion',
      fields: { from_event: '$form_submit', from_path: '/contact', to_event: 'signup' },
    })
    serveAlerts([a])
    const update = vi.spyOn(endpoints, 'updateAlert').mockResolvedValue(a)
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    expect(f.getByLabelText('From event')).toHaveValue('$form_submit')
    expect(f.getByLabelText('From page path')).toHaveValue('/contact')
    expect(screen.getByText('Conversion from $form_submit on /contact to signup')).toBeInTheDocument()
    await vi.waitFor(() => expect(within(f.getByLabelText('From event')).getByRole('option', { name: 'paid' })).toBeInTheDocument())
    await user.selectOptions(f.getByLabelText('From event'), 'paid')
    expect(f.getByLabelText('From page path')).toHaveValue('/contact')
    await user.type(f.getByLabelText('To page path'), '/welcome')
    await user.click(f.getByRole('button', { name: 'Save' }))
    expect(update).toHaveBeenCalledWith(1, 7, { fields: { from_event: 'paid', from_path: '/contact', to_event: 'signup', to_path: '/welcome' } })
  })

  it("sends a field another client changed meanwhile as it now is, beside the one edited here", async () => {
    const a = alert({ id: 7, kind: 'funnel', unit: 'fraction', template: 'conversion', fields: { from_event: '$page_view', to_event: 'signup' } })
    const { lists } = serveAlerts([a])
    const update = vi.spyOn(endpoints, 'updateAlert').mockResolvedValue(a)
    const user = userEvent.setup()
    const { client } = renderPage()
    const f = await form()
    await vi.waitFor(() => expect(within(f.getByLabelText('To event')).getByRole('option', { name: 'paid' })).toBeInTheDocument())
    await user.selectOptions(f.getByLabelText('To event'), 'paid')
    // Another client changed the first step, and the list is read again.
    lists.live = [{ ...a, fields: { from_event: 'signup', to_event: 'signup' } }]
    await client.invalidateQueries({ queryKey: ['alerts', 1] })
    await vi.waitFor(() => expect(f.getByLabelText('From event')).toHaveValue('signup'))
    expect(f.getByLabelText('To event')).toHaveValue('paid')
    await user.click(f.getByRole('button', { name: 'Save' }))
    expect(update).toHaveBeenCalledWith(1, 7, { fields: { from_event: 'signup', to_event: 'paid' } })
  })

  it('keeps Save disabled when the people chosen are the default the alert already counts', async () => {
    serveAlerts([alert({ id: 7, template: 'unique_people', fields: { event: 'signup' } })])
    const user = userEvent.setup()
    renderPage()
    const f = await form()
    await user.selectOptions(f.getByLabelText('Who'), 'users')
    expect(f.getByRole('button', { name: 'Save' })).toBeEnabled()
    await user.selectOptions(f.getByLabelText('Who'), 'actors')
    expect(f.getByRole('button', { name: 'Save' })).toBeDisabled()
  })
})
