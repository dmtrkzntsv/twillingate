import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { ApiError, endpoints } from '@/lib/api'
import { TooltipProvider } from '@/components/ui/tooltip'
import { renderWithProviders } from '@/test/render'
import { monthsRange, utcToday } from '@/lib/usage-months'
import UsageSection from './UsageSection'

const empty = { from: 'a', to: 'b', database_bytes: 0, database_series: [], projects: [{
  project_id: 4, series: [], totals: { views: 0, events: 0, measures: 0, checks: 0 }, last_received_at: null, first_day: null, size: null,
}] }

beforeEach(() => vi.restoreAllMocks())

describe('UsageSection', () => {
  it('shows totals, freshness and the first day, and no size', async () => {
    vi.spyOn(endpoints, 'usage').mockResolvedValue({ from: '2026-09-01', to: '2026-09-02', database_bytes: 0, database_series: [], projects: [{
      project_id: 4,
      series: [{ day: '2026-09-01', views: 10, events: 3, measures: 1, checks: 900, total_bytes: null }, { day: '2026-09-02', views: 20, events: 0, measures: 0, checks: 900, total_bytes: 2_262_000 }],
      totals: { views: 30, events: 3, measures: 1, checks: 1800 },
      last_received_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
      first_day: '2026-08-01',
      size: { total_bytes: 2_262_000, measured_at: new Date(Date.now() - 86_400_000).toISOString().slice(0, 10) },
    }], quota_check_ratio: 0 })
    renderWithProviders(<UsageSection projectId={4} range={{ from: '2026-09-01', to: '2026-09-02' }} />)
    expect(await screen.findByText('34')).toBeInTheDocument()
    expect(screen.getByText('3 h ago')).toBeInTheDocument()
    expect(screen.queryByText(/2\.3 MB|Data size/)).not.toBeInTheDocument()
    expect(screen.getByText('First day')).toBeInTheDocument()
    expect(screen.getByText('2026-08-01')).toBeInTheDocument()
    expect(screen.queryByText('Attributes')).not.toBeInTheDocument()
    expect(screen.queryByText('Days kept')).not.toBeInTheDocument()
    expect(endpoints.usage).toHaveBeenCalledWith({ project_id: 4, from: '2026-09-01', to: '2026-09-02' })
  })

  it('shows an empty project without errors', async () => {
    vi.spyOn(endpoints, 'usage').mockResolvedValue({ from: 'a', to: 'b', database_bytes: 0, database_series: [], projects: [{
      project_id: 5, series: [{ day: 'a', views: 0, events: 0, measures: 0, checks: 720, total_bytes: null }, { day: 'b', views: 0, events: 0, measures: 0, checks: 720, total_bytes: null }], totals: { views: 0, events: 0, measures: 0, checks: 1440 }, last_received_at: null,
      first_day: null, size: null,
    }], quota_check_ratio: 0 })
    renderWithProviders(<UsageSection projectId={5} range={{ from: 'a', to: 'b' }} />)
    expect(await screen.findByText('Nothing received yet')).toBeInTheDocument()
    expect(screen.getByText('No events in this range.')).toBeInTheDocument()
    expect(screen.getByText('nothing kept yet')).toBeInTheDocument()
  })

  it('offers a retry when the stats fail', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(endpoints, 'usage').mockImplementation(async (q) => {
      if (q.from === 'a') throw new ApiError(400, 'range over 400 days; narrow the date range')
      return empty
    })
    renderWithProviders(<UsageSection projectId={4} range={{ from: 'a', to: 'b' }} />)
    expect(await screen.findByText('range over 400 days; narrow the date range')).toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(spy.mock.calls.filter(([q]) => q.from === 'a')).toHaveLength(2)
  })

  it('shows a skeleton on the first load, then keeps the previous numbers while a new range loads', async () => {
    const mk = (views: number) => ({ from: 'a', to: 'b', database_bytes: 0, database_series: [], projects: [{
      project_id: 4, series: [{ day: '2026-09-01', views, events: 0, measures: 0, checks: 0, total_bytes: null }], totals: { views, events: 0, measures: 0, checks: 0 },
      last_received_at: null, first_day: null, size: null,
    }] })
    let release!: () => void
    const second = new Promise<ReturnType<typeof mk>>((res) => { release = () => res(mk(99)) })
    // The monthly chart reads its own range, from the 1st of its oldest month.
    const daily = vi.fn().mockResolvedValueOnce(mk(11)).mockReturnValueOnce(second)
    vi.spyOn(endpoints, 'usage').mockImplementation((q) => (q.from === monthsRange(utcToday()).from ? Promise.resolve(mk(0)) : daily(q)))
    const { client, rerender } = renderWithProviders(<UsageSection projectId={4} range={{ from: '2026-09-01', to: '2026-09-01' }} />)
    expect(screen.getByRole('region', { name: 'Usage' }).querySelector('[data-slot="skeleton"]')).not.toBeNull()
    expect(screen.queryByText(/Couldn't load/)).not.toBeInTheDocument()
    expect(await screen.findByText('11')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Usage' }).querySelector('[data-slot="skeleton"]')).toBeNull()
    rerender(<QueryClientProvider client={client}><TooltipProvider><UsageSection projectId={4} range={{ from: '2026-09-02', to: '2026-09-02' }} /></TooltipProvider></QueryClientProvider>)
    await waitFor(() => expect(daily).toHaveBeenCalledTimes(2))
    expect(screen.getByText('11')).toBeInTheDocument()
    release()
    expect(await screen.findByText('99')).toBeInTheDocument()
  })

  it('stacks the weighted checks by day and counts them in the Events tile, unless the ratio is 0', async () => {
    const answer = (ratio: number) => ({ from: 'a', to: 'b', database_bytes: 0, database_series: [], projects: [{
      project_id: 4, series: [{ day: '2026-09-01', views: 10, events: 0, measures: 0, checks: 100, total_bytes: null }], totals: { views: 10, events: 0, measures: 0, checks: 100 },
      last_received_at: null, first_day: null, size: null,
    }], quota_check_ratio: ratio })
    const daily = (container: HTMLElement) => container.querySelector('[aria-label="Usage"] [data-slot="chart"]:not([data-testid="monthly-usage"])')!
    for (const [ratio, total, hint] of [
      [0, '10', '10 views · 0 product · 0 measures'],
      [0.5, '60', '10 views · 0 product · 0 measures · 50 checks'],
    ] as const) {
      vi.spyOn(endpoints, 'usage').mockResolvedValue(answer(ratio))
      const { container, unmount } = renderWithProviders(<UsageSection projectId={4} range={{ from: '2026-09-01', to: '2026-09-01' }} />)
      await waitFor(() => expect(daily(container)?.querySelector('.recharts-bar-rectangle')).toBeTruthy())
      expect(within(daily(container) as HTMLElement).queryByText('Checks') !== null).toBe(ratio > 0)
      // The Events tile: its value, then the hint that splits it.
      expect(screen.getByText(hint).previousElementSibling).toHaveTextContent(total)
      unmount()
    }
  })

  it('draws a range whose only rows are checks when they count, and calls it empty when they do not', async () => {
    const answer = (ratio: number) => ({ from: 'a', to: 'b', database_bytes: 0, database_series: [], projects: [{
      project_id: 4, series: [{ day: '2026-09-01', views: 0, events: 0, measures: 0, checks: 720, total_bytes: null }], totals: { views: 0, events: 0, measures: 0, checks: 720 },
      last_received_at: null, first_day: null, size: null,
    }], quota_check_ratio: ratio })
    vi.spyOn(endpoints, 'usage').mockResolvedValue(answer(0.5))
    const first = renderWithProviders(<UsageSection projectId={4} range={{ from: '2026-09-01', to: '2026-09-01' }} />)
    expect(await screen.findByText('0 views · 0 product · 0 measures · 360 checks')).toBeInTheDocument()
    expect(screen.queryByText('No events in this range.')).not.toBeInTheDocument()
    first.unmount()
    vi.spyOn(endpoints, 'usage').mockResolvedValue(answer(0))
    renderWithProviders(<UsageSection projectId={4} range={{ from: '2026-09-01', to: '2026-09-01' }} />)
    expect(await screen.findByText('No events in this range.')).toBeInTheDocument()
  })
})
