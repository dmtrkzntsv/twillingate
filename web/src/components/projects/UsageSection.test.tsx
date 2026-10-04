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
  it('shows totals, freshness and size', async () => {
    vi.spyOn(endpoints, 'usage').mockResolvedValue({ from: '2026-09-01', to: '2026-09-02', database_bytes: 0, database_series: [], projects: [{
      project_id: 4,
      series: [{ day: '2026-09-01', views: 10, events: 3, measures: 1, total_bytes: null, declared_attributes: null, attribute_keys: null, attribute_values: null, attribute_values_folded: null }, { day: '2026-09-02', views: 20, events: 0, measures: 0, total_bytes: 2_262_000, declared_attributes: null, attribute_keys: null, attribute_values: null, attribute_values_folded: null }],
      totals: { views: 30, events: 3, measures: 1 },
      last_received_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
      first_day: '2026-08-01', raw_days: 30, rolled_up_days: 2,
      size: { raw_bytes: 812_000, aggregate_bytes: 1_450_000, total_bytes: 2_262_000,
        measured_at: new Date(Date.now() - 86_400_000).toISOString().slice(0, 10) },
      unused_attributes: ['self_hosted'],
    }] })
    renderWithProviders(<UsageSection projectId={4} range={{ from: '2026-09-01', to: '2026-09-02' }} />)
    expect(await screen.findByText('34')).toBeInTheDocument()
    expect(screen.getByText('3 h ago')).toBeInTheDocument()
    expect(screen.getByText('2.3 MB')).toBeInTheDocument()
    expect(screen.getByText(/estimate, measured yesterday/)).toBeInTheDocument()
    expect(screen.queryByText('No size measured in this range.')).not.toBeInTheDocument()
    expect(screen.getByText(/30 raw · 2 rolled up/)).toBeInTheDocument()
    expect(screen.queryByText(/Declared but not sent/)).not.toBeInTheDocument()
    expect(endpoints.usage).toHaveBeenCalledWith({ project_id: 4, from: '2026-09-01', to: '2026-09-02' })
  })

  it('sums up the attributes the daily pass stored', async () => {
    const day = (d: string, declared: number | null, keys: number | null, values: number | null, folded: number | null) => ({
      day: d, views: 1, events: 0, measures: 0, total_bytes: null,
      declared_attributes: declared, attribute_keys: keys, attribute_values: values, attribute_values_folded: folded,
    })
    vi.spyOn(endpoints, 'usage').mockResolvedValue({ from: 'a', to: 'c', database_bytes: 0, database_series: [], projects: [{
      project_id: 4, series: [day('a', 2, 4, 90, 0), day('b', 3, 6, 1_200, 15), day('c', null, null, null, null)],
      totals: { views: 3, events: 0, measures: 0 }, last_received_at: null,
      first_day: null, raw_days: 3, rolled_up_days: 0, size: null, unused_attributes: [],
    }] })
    renderWithProviders(<UsageSection projectId={4} range={{ from: 'a', to: 'c' }} />)
    expect(await screen.findByText('3 declared')).toBeInTheDocument()
    expect(screen.getByText('up to 6 keys and 1,200 values a day · 15 folded into (other)')).toBeInTheDocument()
  })

  it('says attributes are counted nightly before the first count', async () => {
    vi.spyOn(endpoints, 'usage').mockResolvedValue({ from: 'a', to: 'a', database_bytes: 0, database_series: [], projects: [{
      project_id: 4, series: [{ day: 'a', views: 1, events: 0, measures: 0, total_bytes: null,
        declared_attributes: null, attribute_keys: null, attribute_values: null, attribute_values_folded: null }],
      totals: { views: 1, events: 0, measures: 0 }, last_received_at: null,
      first_day: null, raw_days: 1, rolled_up_days: 0, size: null, unused_attributes: [],
    }] })
    renderWithProviders(<UsageSection projectId={4} range={{ from: 'a', to: 'a' }} />)
    expect(await screen.findByText('received values are counted nightly')).toBeInTheDocument()
  })

  it('shows an empty project without errors', async () => {
    vi.spyOn(endpoints, 'usage').mockResolvedValue({ from: 'a', to: 'b', database_bytes: 0, database_series: [], projects: [{
      project_id: 5, series: [{ day: 'a', views: 0, events: 0, measures: 0, total_bytes: null, declared_attributes: null, attribute_keys: null, attribute_values: null, attribute_values_folded: null }, { day: 'b', views: 0, events: 0, measures: 0, total_bytes: null, declared_attributes: null, attribute_keys: null, attribute_values: null, attribute_values_folded: null }], totals: { views: 0, events: 0, measures: 0 }, last_received_at: null,
      first_day: null, raw_days: 0, rolled_up_days: 0, size: null, unused_attributes: [],
    }] })
    renderWithProviders(<UsageSection projectId={5} range={{ from: 'a', to: 'b' }} />)
    expect(await screen.findByText('Nothing received yet')).toBeInTheDocument()
    expect(screen.getByText('Not measured yet')).toBeInTheDocument()
    expect(screen.getByText('No events in this range.')).toBeInTheDocument()
    expect(screen.getByText('No size measured in this range.')).toBeInTheDocument()
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
      project_id: 4, series: [{ day: '2026-09-01', views, events: 0, measures: 0, total_bytes: null, declared_attributes: null, attribute_keys: null, attribute_values: null, attribute_values_folded: null }], totals: { views, events: 0, measures: 0 },
      last_received_at: null, first_day: null, raw_days: 1, rolled_up_days: 0, size: null, unused_attributes: [],
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
