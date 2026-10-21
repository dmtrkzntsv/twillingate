import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { ApiError, endpoints } from '@/lib/api'
import { TooltipProvider } from '@/components/ui/tooltip'
import { renderWithProviders } from '@/test/render'
import UsageSection from './UsageSection'

beforeEach(() => vi.restoreAllMocks())

describe('UsageSection', () => {
  it('shows totals, freshness, size and the first day', async () => {
    vi.spyOn(endpoints, 'usage').mockResolvedValue({ from: '2026-09-01', to: '2026-09-02', database_bytes: 0, database_series: [], projects: [{
      project_id: 4,
      series: [{ day: '2026-09-01', views: 10, events: 3, measures: 1, total_bytes: null }, { day: '2026-09-02', views: 20, events: 0, measures: 0, total_bytes: 2_262_000 }],
      totals: { views: 30, events: 3, measures: 1 },
      last_received_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
      first_day: '2026-08-01',
      size: { total_bytes: 2_262_000, measured_at: new Date(Date.now() - 86_400_000).toISOString().slice(0, 10) },
    }] })
    renderWithProviders(<UsageSection projectId={4} range={{ from: '2026-09-01', to: '2026-09-02' }} />)
    expect(await screen.findByText('34')).toBeInTheDocument()
    expect(screen.getByText('3 h ago')).toBeInTheDocument()
    expect(screen.getByText('2.3 MB')).toBeInTheDocument()
    expect(screen.getByText('event files, measured yesterday')).toBeInTheDocument()
    expect(screen.queryByText('No size measured in this range.')).not.toBeInTheDocument()
    expect(screen.getByText('First day')).toBeInTheDocument()
    expect(screen.getByText('2026-08-01')).toBeInTheDocument()
    expect(screen.queryByText('Attributes')).not.toBeInTheDocument()
    expect(screen.queryByText('Days kept')).not.toBeInTheDocument()
    expect(endpoints.usage).toHaveBeenCalledWith({ project_id: 4, from: '2026-09-01', to: '2026-09-02' })
  })

  it('shows an empty project without errors', async () => {
    vi.spyOn(endpoints, 'usage').mockResolvedValue({ from: 'a', to: 'b', database_bytes: 0, database_series: [], projects: [{
      project_id: 5, series: [{ day: 'a', views: 0, events: 0, measures: 0, total_bytes: null }, { day: 'b', views: 0, events: 0, measures: 0, total_bytes: null }], totals: { views: 0, events: 0, measures: 0 }, last_received_at: null,
      first_day: null, size: null,
    }] })
    renderWithProviders(<UsageSection projectId={5} range={{ from: 'a', to: 'b' }} />)
    expect(await screen.findByText('Nothing received yet')).toBeInTheDocument()
    expect(screen.getByText('Not measured yet')).toBeInTheDocument()
    expect(screen.getByText('No events in this range.')).toBeInTheDocument()
    expect(screen.getByText('No size measured in this range.')).toBeInTheDocument()
    expect(screen.getByText('nothing kept yet')).toBeInTheDocument()
  })

  it('offers a retry when the stats fail', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(endpoints, 'usage').mockRejectedValue(new ApiError(400, 'range over 400 days; narrow the date range'))
    renderWithProviders(<UsageSection projectId={4} range={{ from: 'a', to: 'b' }} />)
    expect(await screen.findByText('range over 400 days; narrow the date range')).toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(spy).toHaveBeenCalledTimes(2)
  })

  it('shows a skeleton on the first load, then keeps the previous numbers while a new range loads', async () => {
    const mk = (views: number) => ({ from: 'a', to: 'b', database_bytes: 0, database_series: [], projects: [{
      project_id: 4, series: [{ day: '2026-09-01', views, events: 0, measures: 0, total_bytes: null }], totals: { views, events: 0, measures: 0 },
      last_received_at: null, first_day: null, size: null,
    }] })
    let release!: () => void
    const second = new Promise<ReturnType<typeof mk>>((res) => { release = () => res(mk(99)) })
    vi.spyOn(endpoints, 'usage').mockResolvedValueOnce(mk(11)).mockReturnValueOnce(second)
    const { client, rerender } = renderWithProviders(<UsageSection projectId={4} range={{ from: '2026-09-01', to: '2026-09-01' }} />)
    expect(screen.getByRole('region', { name: 'Usage' }).querySelector('[data-slot="skeleton"]')).not.toBeNull()
    expect(screen.queryByText(/Couldn't load/)).not.toBeInTheDocument()
    expect(await screen.findByText('11')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Usage' }).querySelector('[data-slot="skeleton"]')).toBeNull()
    rerender(<QueryClientProvider client={client}><TooltipProvider><UsageSection projectId={4} range={{ from: '2026-09-02', to: '2026-09-02' }} /></TooltipProvider></QueryClientProvider>)
    await waitFor(() => expect(endpoints.usage).toHaveBeenCalledTimes(2))
    expect(screen.getByText('11')).toBeInTheDocument()
    release()
    expect(await screen.findByText('99')).toBeInTheDocument()
  })
})
