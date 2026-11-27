import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { toast } from 'sonner'
import { ApiError, endpoints, type Alert, type AlertPreview, type AlertsResponse } from '@/lib/api'
import { alertsQuery } from '@/lib/queries'
import { alert } from '@/test/alerts'
import { renderWithProviders } from '@/test/render'
import AlertsList from './AlertsList'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))

const PREVIEW: AlertPreview = {
  days: [
    { day: '2026-11-24', value: 7, compare: null, volume: null, outcome: 'ok' },
    { day: '2026-11-25', value: 3, compare: null, volume: null, outcome: 'breached' },
    { day: '2026-11-26', value: 9, compare: null, volume: null, outcome: 'ok' },
  ],
  gaps: [],
  fired: 1,
}

/** The alert create_alert answers for the event_count walk-through. */
const MADE = alert({ id: 42, name: 'Count of signup below 5 over 1 day', template: 'event_count', fields: { event: 'signup' }, condition: { type: 'threshold', op: 'below', value: 5 } })

let events: string[]
/** The live alerts `endpoints.alerts` answers, which a create adds to, as the server would. */
let live: Alert[]

beforeEach(() => {
  vi.restoreAllMocks()
  vi.mocked(toast).mockClear()
  vi.mocked(toast.success).mockClear()
  vi.mocked(toast.error).mockClear()
  events = ['signup', 'paid']
  live = []
  vi.spyOn(endpoints, 'alerts').mockImplementation(async () => ({ max_live: 100, alerts: live }))
  vi.spyOn(endpoints, 'previewAlert').mockResolvedValue(PREVIEW)
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC' } as Awaited<ReturnType<typeof endpoints.dashboards>>)
  vi.spyOn(endpoints, 'eventNames').mockImplementation(async () => ({ events: events.map((name) => ({ family: 'product', name, events: 10 })) }))
})

/** Where the router is, for a test to read after a navigation. */
function Where() {
  const loc = useLocation()
  return <p data-testid="where">{loc.pathname + loc.search}</p>
}

function renderList(opts: { path?: string; projectArchived?: boolean } = {}) {
  return renderWithProviders(
    <MemoryRouter initialEntries={[opts.path ?? '/projects/1/alerts?range=7d']}>
      <Routes>
        <Route path="/projects/:id/alerts" element={<AlertsList projectId={1} projectArchived={opts.projectArchived} />} />
        <Route path="/projects/:id/alerts/:alertId" element={<Where />} />
      </Routes>
    </MemoryRouter>
  )
}

async function open(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'New alert' }))
  return within(await screen.findByRole('dialog', { name: 'New alert' }))
}

/** Walks the wizard to step 3 of an event_count alert on `event`, below `value`. */
async function toStepThree(user: ReturnType<typeof userEvent.setup>, event = 'signup', value = '5') {
  const d = await open(user)
  await user.click(d.getByRole('button', { name: /^Event count/ }))
  await d.findByRole('option', { name: event })
  await user.selectOptions(d.getByLabelText('Event'), event)
  await user.type(d.getByLabelText('Threshold'), value)
  await user.click(d.getByRole('button', { name: 'Next' }))
  await d.findByText('Step 3 of 3')
  return d
}

describe('NewAlertDialog', () => {
  it('opens from the list and offers the four templates in order', async () => {
    const user = userEvent.setup()
    renderList()
    const d = await open(user)
    expect(d.getByText('Step 1 of 3')).toBeInTheDocument()
    const choices = d.getAllByRole('button', { pressed: false })
    expect(choices.map((b) => b.querySelector('[data-title]')?.textContent)).toEqual(['Event count', 'Unique people', 'Conversion', 'No events'])
    expect(choices[0]).toHaveTextContent('How many times an event happens a day, or over a few days.')
    expect(choices[3]).toHaveTextContent('An event stops arriving for some hours.')
  })

  it('opens beside a list of alerts too', async () => {
    vi.mocked(endpoints.alerts).mockResolvedValue({ max_live: 100, alerts: [alert({ id: 3 })] })
    const user = userEvent.setup()
    renderList()
    await screen.findByRole('list', { name: 'Alerts' })
    const d = await open(user)
    expect(d.getByText('Step 1 of 3')).toBeInTheDocument()
  })

  it('creates an event_count alert: backtests it, suggests its name, creates it once on a double click and opens its page', async () => {
    let answer!: (a: Alert) => void
    const create = vi.spyOn(endpoints, 'createAlert').mockImplementation(() => new Promise((resolve) => (answer = resolve)))
    const user = userEvent.setup()
    const { client } = renderList()
    const d = await open(user)
    await user.click(d.getByRole('button', { name: /^Event count/ }))
    expect(d.getByText('Step 2 of 3')).toBeInTheDocument()
    const next = d.getByRole('button', { name: 'Next' })
    expect(next).toBeDisabled()
    await d.findByRole('option', { name: 'signup' })
    await user.selectOptions(d.getByLabelText('Event'), 'signup')
    expect(next).toBeDisabled()
    await user.type(d.getByLabelText('Threshold'), '5')
    expect(next).toBeEnabled()
    await user.click(next)

    expect(d.getByText('Step 3 of 3')).toBeInTheDocument()
    expect(endpoints.previewAlert).toHaveBeenCalledWith(1, {
      template: 'event_count',
      fields: { event: 'signup' },
      condition: { type: 'threshold', op: 'below', value: 5 },
      window_days: 1,
    })
    expect(await d.findByText('Would have fired 1 time in the last 3 days')).toBeInTheDocument()
    expect(d.getByLabelText('Name')).toHaveValue('Count of signup below 5 over 1 day')

    await user.dblClick(d.getByRole('button', { name: 'Create alert' }))
    expect(d.getByRole('button', { name: 'Create alert' })).toBeDisabled()
    live = [MADE]
    answer(MADE)
    await waitFor(() => expect(screen.getByTestId('where')).toHaveTextContent('/projects/1/alerts/42?range=7d'))
    expect(create).toHaveBeenCalledTimes(1)
    expect(create).toHaveBeenCalledWith(1, {
      name: 'Count of signup below 5 over 1 day',
      template: 'event_count',
      fields: { event: 'signup' },
      condition: { type: 'threshold', op: 'below', value: 5 },
      window_days: 1,
    })
    expect(toast.success).toHaveBeenCalledWith('Alert created')
    expect(toast).not.toHaveBeenCalled()
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(client.getQueryData<AlertsResponse>(alertsQuery(1, false).queryKey)?.alerts).toEqual([MADE])
  })

  it('backtests the new definition after Back, a change and Next, reusing an unchanged one', async () => {
    const user = userEvent.setup()
    renderList()
    const d = await toStepThree(user)
    await d.findByText('Would have fired 1 time in the last 3 days')
    expect(endpoints.previewAlert).toHaveBeenCalledTimes(1)

    await user.click(d.getByRole('button', { name: 'Back' }))
    expect(d.getByText('Step 2 of 3')).toBeInTheDocument()
    expect(d.getByLabelText('Event')).toHaveValue('signup')
    expect(d.getByLabelText('Threshold')).toHaveValue('5')
    await user.click(d.getByRole('button', { name: 'Next' }))
    await d.findByText('Would have fired 1 time in the last 3 days')
    expect(endpoints.previewAlert).toHaveBeenCalledTimes(1)

    await user.click(d.getByRole('button', { name: 'Back' }))
    await user.selectOptions(d.getByLabelText('Event'), 'paid')
    await user.click(d.getByRole('button', { name: 'Next' }))
    await waitFor(() => expect(endpoints.previewAlert).toHaveBeenCalledTimes(2))
    expect(endpoints.previewAlert).toHaveBeenLastCalledWith(1, {
      template: 'event_count',
      fields: { event: 'paid' },
      condition: { type: 'threshold', op: 'below', value: 5 },
      window_days: 1,
    })
    // Still the suggestion, so it follows the new definition.
    expect(d.getByLabelText('Name')).toHaveValue('Count of paid below 5 over 1 day')
  })

  it('keeps a typed name across Back and Next', async () => {
    const user = userEvent.setup()
    renderList()
    const d = await toStepThree(user)
    await user.clear(d.getByLabelText('Name'))
    await user.type(d.getByLabelText('Name'), 'Signups')
    await user.click(d.getByRole('button', { name: 'Back' }))
    await user.selectOptions(d.getByLabelText('Event'), 'paid')
    await user.click(d.getByRole('button', { name: 'Next' }))
    expect(d.getByLabelText('Name')).toHaveValue('Signups')
  })

  it('says a refused name under Name and stays on step 3 with every value', async () => {
    vi.spyOn(endpoints, 'createAlert').mockRejectedValue(new ApiError(409, 'a live alert named Count of signup below 5 over 1 day exists', 'conflict'))
    const user = userEvent.setup()
    renderList()
    const d = await toStepThree(user)
    await user.click(d.getByRole('button', { name: 'Create alert' }))
    const msg = await d.findByText('a live alert named Count of signup below 5 over 1 day exists')
    const name = d.getByLabelText('Name')
    expect(name).toHaveAttribute('aria-invalid', 'true')
    expect(name.getAttribute('aria-describedby')).toBe(msg.id)
    expect(name).toHaveValue('Count of signup below 5 over 1 day')
    expect(d.getByText('Step 3 of 3')).toBeInTheDocument()
    expect(d.getByText('Would have fired 1 time in the last 3 days')).toBeInTheDocument()
    expect(toast.error).not.toHaveBeenCalled()
    expect(d.getByRole('button', { name: 'Create alert' })).toBeEnabled()

    // A new name clears it.
    await user.type(name, ' 2')
    expect(d.queryByText('a live alert named Count of signup below 5 over 1 day exists')).toBeNull()

    await user.click(d.getByRole('button', { name: 'Back' }))
    expect(d.getByLabelText('Event')).toHaveValue('signup')
    expect(d.getByLabelText('Threshold')).toHaveValue('5')
  })

  it('says any other refusal under the step, until the name changes', async () => {
    vi.spyOn(endpoints, 'createAlert').mockRejectedValue(new ApiError(400, 'the event files could not be read', 'invalid'))
    const user = userEvent.setup()
    renderList()
    const d = await toStepThree(user)
    await user.click(d.getByRole('button', { name: 'Create alert' }))
    expect(await d.findByRole('alert')).toHaveTextContent('the event files could not be read')
    expect(d.getByLabelText('Name')).not.toHaveAttribute('aria-invalid')
    expect(d.getByText('Step 3 of 3')).toBeInTheDocument()
    await user.type(d.getByLabelText('Name'), ' 2')
    expect(d.queryByRole('alert')).toBeNull()
  })

  it('refuses a name past 100 characters before sending it', async () => {
    const create = vi.spyOn(endpoints, 'createAlert').mockResolvedValue(MADE)
    const user = userEvent.setup()
    renderList()
    const d = await toStepThree(user)
    const name = d.getByLabelText('Name')
    await user.clear(name)
    await user.click(name)
    await user.paste('a'.repeat(101))
    const msg = d.getByText('At most 100 characters')
    expect(name).toHaveAttribute('aria-invalid', 'true')
    expect(name.getAttribute('aria-describedby')).toBe(msg.id)
    const button = d.getByRole('button', { name: 'Create alert' })
    expect(button).toBeDisabled()
    // Enter submits the form all the same: nothing is sent.
    await user.type(name, '{Enter}')
    expect(create).not.toHaveBeenCalled()

    await user.type(name, '{Backspace}')
    expect(d.queryByText('At most 100 characters')).toBeNull()
    expect(button).toBeEnabled()
  })

  it('toasts the events the project never sent', async () => {
    vi.spyOn(endpoints, 'createAlert').mockResolvedValue({
      ...MADE,
      warnings: ['`signup` has never been sent by this project; the alert reads no_data until it is'],
    })
    const user = userEvent.setup()
    renderList()
    const d = await toStepThree(user)
    await user.click(d.getByRole('button', { name: 'Create alert' }))
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Never sent yet: signup'))
    expect(toast.success).toHaveBeenCalledWith('Alert created')
  })

  it('creates an event_count alert on page views of a path when the project sent no product event', async () => {
    events = []
    const create = vi.spyOn(endpoints, 'createAlert').mockResolvedValue(MADE)
    const user = userEvent.setup()
    renderList()
    const d = await open(user)
    await user.click(d.getByRole('button', { name: /^Event count/ }))
    await user.selectOptions(d.getByLabelText('Event'), '$page_view')
    await user.type(d.getByLabelText('Page path'), '/pricing')
    await user.type(d.getByLabelText('Threshold'), '1')
    await user.click(d.getByRole('button', { name: 'Next' }))
    const body = { template: 'event_count', fields: { event: '$page_view', path: '/pricing' }, condition: { type: 'threshold', op: 'below', value: 1 }, window_days: 1 }
    expect(endpoints.previewAlert).toHaveBeenCalledWith(1, body)
    expect(d.getByLabelText('Name')).toHaveValue('Count of $page_view on /pricing below 1 over 1 day')
    await user.click(d.getByRole('button', { name: 'Create alert' }))
    await waitFor(() => expect(create).toHaveBeenCalledWith(1, { name: 'Count of $page_view on /pricing below 1 over 1 day', ...body }))
  })

  it('asks no_events for hours alone, with no window', async () => {
    const user = userEvent.setup()
    renderList()
    const d = await open(user)
    await user.click(d.getByRole('button', { name: /^No events/ }))
    expect(d.getByLabelText('Hours without an event')).toHaveValue('6')
    expect(d.queryByLabelText('Window')).toBeNull()
    expect(d.queryByLabelText('Condition')).toBeNull()
    expect(d.queryByLabelText('Threshold')).toBeNull()
    await d.findByRole('option', { name: 'signup' })
    await user.selectOptions(d.getByLabelText('Event'), 'signup')
    await user.click(d.getByRole('button', { name: 'Next' }))
    expect(endpoints.previewAlert).toHaveBeenCalledWith(1, { template: 'no_events', fields: { event: 'signup' }, condition: { type: 'silence', hours: 6 } })
    expect(d.getByLabelText('Name')).toHaveValue('No signup for 6 hours')
  })

  it('starts another template from its blank, and keeps what was entered when the same one is chosen again', async () => {
    const user = userEvent.setup()
    renderList()
    const d = await open(user)
    await user.click(d.getByRole('button', { name: /^Event count/ }))
    await d.findByRole('option', { name: 'signup' })
    await user.selectOptions(d.getByLabelText('Event'), 'signup')
    await user.type(d.getByLabelText('Threshold'), '5')
    await user.click(d.getByRole('button', { name: 'Back' }))
    expect(d.getByRole('button', { name: /^Event count/ })).toHaveAttribute('aria-pressed', 'true')
    await user.click(d.getByRole('button', { name: /^Event count/ }))
    expect(d.getByLabelText('Event')).toHaveValue('signup')
    expect(d.getByLabelText('Threshold')).toHaveValue('5')
    await user.click(d.getByRole('button', { name: 'Back' }))
    await user.click(d.getByRole('button', { name: /^Conversion/ }))
    expect(d.getByLabelText('From event')).toHaveValue('')
    expect(d.getByLabelText('Threshold')).toHaveValue('')
    expect(d.getByLabelText('Window')).toHaveValue('7')
  })

  it('discards what was entered when closed', async () => {
    const user = userEvent.setup()
    renderList()
    let d = await open(user)
    await user.click(d.getByRole('button', { name: /^Event count/ }))
    await user.click(d.getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    d = await open(user)
    expect(d.getByText('Step 1 of 3')).toBeInTheDocument()
    expect(d.getByRole('button', { name: /^Event count/ })).toHaveAttribute('aria-pressed', 'false')
  })

  it('keeps New alert from a project at its live-alert limit, saying why', async () => {
    vi.mocked(endpoints.alerts).mockResolvedValue({ max_live: 2, alerts: [alert({ id: 3, name: 'A' }), alert({ id: 4, name: 'B' })] })
    const user = userEvent.setup()
    renderList()
    const button = await screen.findByRole('button', { name: 'New alert' })
    expect(button).toHaveAttribute('aria-disabled', 'true')
    await user.hover(button)
    expect(await screen.findByRole('tooltip')).toHaveTextContent('A project holds at most 2 live alerts')
    await user.click(button)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('offers no New alert on an archived project', async () => {
    renderList({ projectArchived: true })
    const empty = await screen.findByRole('region', { name: 'No alerts yet' })
    expect(empty).toHaveTextContent('An archived project takes no new alerts: restore it to create one.')
    expect(screen.queryByRole('button', { name: 'New alert' })).toBeNull()
  })

  it('offers no New alert in the Archived list', async () => {
    vi.mocked(endpoints.alerts).mockResolvedValue({ max_live: 100, alerts: [alert({ id: 3, archived_at: '2026-11-20T10:00:00Z' })] })
    renderList({ path: '/projects/1/alerts?show=archived' })
    await screen.findByRole('list', { name: 'Alerts' })
    expect(screen.queryByRole('button', { name: 'New alert' })).toBeNull()
  })
})
