import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { toast } from 'sonner'
import { ApiError, endpoints, type Project } from '@/lib/api'
import { alert, brokenAlert, serveAlerts } from '@/test/alerts'
import { alertsQuery } from '@/lib/queries'
import { renderWithProviders, testClient } from '@/test/render'
import AlertPage from './AlertPage'

vi.mock('sonner', () => ({ toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }) }))

const project: Project = { project_id: 1, name: 'shop', allowed_origins: [] }

beforeEach(() => {
  vi.restoreAllMocks()
  vi.mocked(toast).mockClear()
  // The backtest (and a template alert's event list) read on their own: answered empty.
  vi.spyOn(endpoints, 'previewAlert').mockResolvedValue({ days: [], gaps: [], fired: 0 })
  vi.spyOn(endpoints, 'eventNames').mockResolvedValue({ events: [] })
  vi.spyOn(endpoints, 'dashboards').mockResolvedValue({ timezone: 'UTC' } as Awaited<ReturnType<typeof endpoints.dashboards>>)
})

function renderPage(alertId: string) {
  return renderWithProviders(
    <MemoryRouter initialEntries={[`/projects/1/alerts/${alertId}?range=7d`]}>
      <AlertPage project={project} alertId={alertId} />
    </MemoryRouter>
  )
}

const facts = () => screen.getByTestId('alert-facts')

describe('AlertPage', () => {
  it('finds a live alert and shows its state, facts, actions and definition', async () => {
    const checked = new Date(Date.now() - 3 * 3_600_000).toISOString()
    const a = alert({ id: 7, name: 'Signups', window_days: 7, last_day: '2026-11-24', last_value_text: '120', last_evaluated_at: checked })
    const { spy } = serveAlerts([alert({ id: 3, name: 'Other' }), a])
    renderPage('7')
    expect(await screen.findByRole('heading', { level: 2, name: 'Signups' })).toBeInTheDocument()
    expect(screen.getByText('OK')).toBeInTheDocument()
    expect(facts()).toHaveTextContent('Last value 120 · checked 3 h ago · days 2026-11-18 to 2026-11-24')
    expect(screen.getByRole('button', { name: 'Check now' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pause' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Snooze' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Archive' })).toBeInTheDocument()
    const watches = screen.getByRole('region', { name: 'Watches' })
    expect(within(watches).getByText('count of a stat query')).toBeInTheDocument()
    expect(watches.querySelector('pre')?.textContent).toBe(JSON.stringify(a.query, null, 2))
    // The live list had it: the archived one is not asked for.
    expect(spy).toHaveBeenCalledWith(1, false)
    expect(spy).not.toHaveBeenCalledWith(1, true)
  })

  it('finds an archived alert the live list lacks, with only Restore', async () => {
    serveAlerts([alert({ id: 3, name: 'Other' })], [alert({ id: 7, name: 'Old signups', archived_at: '2026-11-20T10:00:00Z' })])
    renderPage('7')
    expect(await screen.findByRole('heading', { level: 2, name: 'Old signups' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Restore' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Check now' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Pause' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Snooze' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Archive' })).toBeNull()
    expect(facts()).toHaveTextContent('Archived 2026-11-20')
  })

  it('says no alert is there when neither list has it, with a way to the alerts', async () => {
    serveAlerts([alert({ id: 3 })], [alert({ id: 4, archived_at: '2026-11-20T10:00:00Z' })])
    renderPage('12')
    expect(await screen.findByRole('heading', { name: 'No alert at this address' })).toBeInTheDocument()
    expect(screen.getByText('It may have been purged after it was archived, or the link may be wrong.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go to the alerts' })).toHaveAttribute('href', '/projects/1/alerts?range=7d')
  })

  it('says no alert is there at once for an address that is not an id', () => {
    const { spy } = serveAlerts([alert({ id: 3 })])
    renderPage('abc')
    expect(screen.getByRole('heading', { name: 'No alert at this address' })).toBeInTheDocument()
    expect(spy).not.toHaveBeenCalled()
  })

  it('shows a broken alert: its name, Broken, the reason and the stored query', async () => {
    const query = { type: 'funnel', steps: [{ event: 'checkout' }, { event: 'paid' }] }
    serveAlerts([brokenAlert({ id: 9, query })])
    renderPage('9')
    expect(await screen.findByRole('heading', { level: 2, name: 'Old funnel' })).toBeInTheDocument()
    expect(screen.getByText('Broken')).toBeInTheDocument()
    expect(screen.getByText('A rule renamed checkout to purchase')).toBeInTheDocument()
    const watches = screen.getByRole('region', { name: 'Watches' })
    expect(within(watches).getByText('A definition that no longer holds')).toBeInTheDocument()
    expect(watches.querySelector('pre')?.textContent).toBe(JSON.stringify(query, null, 2))
    // No window to read days from.
    expect(facts()).not.toHaveTextContent(/days .+ to /)
  })

  it("shows a broken alert's history, with the error that broke it", async () => {
    serveAlerts([brokenAlert({ id: 9 })])
    const history = vi.spyOn(endpoints, 'alertHistory').mockResolvedValue({
      evaluations: [{ id: 1, at: '2026-11-25T06:00:00Z', from: '', to: '', value: 4.5, compare: null, volume: null, outcome: 'broken', error: 'unknown event purchase' }],
    })
    renderPage('9')
    const region = await screen.findByRole('region', { name: 'History' })
    expect(history).toHaveBeenCalledWith(1, 9, undefined)
    expect(await within(region).findByText('unknown event purchase')).toBeInTheDocument()
    expect(within(region).getByText('4.5')).toBeInTheDocument()
  })

  it("shows a no-data alert's reason, and no reason for an ok one", async () => {
    serveAlerts([alert({ id: 7, state: 'no_data', reason: 'Under the minimum volume' }), alert({ id: 8, name: 'Fine', reason: 'stale' })])
    const { unmount } = renderPage('7')
    expect(await screen.findByText('Under the minimum volume')).toBeInTheDocument()
    unmount()
    renderPage('8')
    await screen.findByRole('heading', { level: 2, name: 'Fine' })
    expect(screen.queryByText('stale')).toBeNull()
  })

  it("shows a silence alert's filters as its query, and no days", async () => {
    const where = [{ field: 'event', op: 'eq', value: 'signup' }]
    serveAlerts([
      alert({ id: 7, kind: 'silence', unit: 'hours', where, condition: { type: 'silence', hours: 6 }, last_day: '', last_value: 2, last_value_text: 'last event 2h ago' }),
    ])
    renderPage('7')
    const watches = await screen.findByRole('region', { name: 'Watches' })
    expect(within(watches).getByText('Events matching 1 filter')).toBeInTheDocument()
    expect(watches.querySelector('pre')?.textContent).toBe(JSON.stringify(where, null, 2))
    expect(facts()).toHaveTextContent(/^Last event 2h ago/)
    expect(facts()).not.toHaveTextContent('Last value')
    expect(facts()).not.toHaveTextContent(/days .+ to /)
  })

  it('shows what a template alert watches, without its query', async () => {
    serveAlerts([alert({ id: 7, template: 'event_count', fields: { event: 'signup' } })])
    renderPage('7')
    const watches = await screen.findByRole('region', { name: 'Watches' })
    expect(within(watches).getByText('Count of signup')).toBeInTheDocument()
    expect(watches.querySelector('pre')).toBeNull()
    expect(within(watches).queryByText('Query')).toBeNull()
  })

  it('says why the alerts did not load, with Retry', async () => {
    vi.spyOn(endpoints, 'alerts').mockRejectedValue(new ApiError(500, 'boom', 'internal'))
    renderPage('7')
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load the alert. boom")
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })

  it('says why the alerts did not load, with Retry, when the lists it had lack the alert and reading them afresh failed', async () => {
    const client = testClient()
    client.setQueryData(alertsQuery(1, false).queryKey, { max_live: 50, alerts: [alert({ id: 3 })] })
    client.setQueryData(alertsQuery(1, true).queryKey, { max_live: 50, alerts: [] })
    vi.spyOn(endpoints, 'alerts').mockRejectedValue(new ApiError(500, 'boom', 'internal'))
    renderWithProviders(
      <MemoryRouter initialEntries={['/projects/1/alerts/7']}>
        <AlertPage project={project} alertId="7" />
      </MemoryRouter>,
      client
    )
    expect(await screen.findByRole('alert')).toHaveTextContent("Couldn't load the alert. boom")
    expect(screen.getByRole('button', { name: 'Retry' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'No alert at this address' })).toBeNull()
  })

  it('polls every 2 s while a check it did not ask for is pending, then stops, without a toast', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    try {
      const { lists, spy } = serveAlerts([alert({ id: 7, check_pending: true })])
      renderPage('7')
      const button = await screen.findByRole('button', { name: 'Checking…' })
      expect(button).toHaveAttribute('aria-disabled', 'true')
      const before = spy.mock.calls.length
      await vi.advanceTimersByTimeAsync(2_100)
      expect(spy.mock.calls.length).toBe(before + 1)
      await vi.advanceTimersByTimeAsync(2_000)
      expect(spy.mock.calls.length).toBe(before + 2)
      lists.live = [alert({ id: 7, state: 'breached' })]
      await vi.advanceTimersByTimeAsync(2_000)
      expect(await screen.findByRole('button', { name: 'Check now' })).not.toHaveAttribute('aria-disabled')
      const settled = spy.mock.calls.length
      await vi.advanceTimersByTimeAsync(6_000)
      expect(spy.mock.calls.length).toBe(settled)
      expect(toast).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })
})
