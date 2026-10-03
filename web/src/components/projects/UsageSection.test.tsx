import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { endpoints } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import UsageSection from './UsageSection'

beforeEach(() => vi.restoreAllMocks())

describe('UsageSection', () => {
  it('shows totals, freshness, size and unused attributes', async () => {
    vi.spyOn(endpoints, 'stats').mockResolvedValue({ from: '2026-09-01', to: '2026-09-02', database_bytes: 0, projects: [{
      project_id: 4,
      series: [{ day: '2026-09-01', views: 10, events: 3, measures: 1 }, { day: '2026-09-02', views: 20, events: 0, measures: 0 }],
      totals: { views: 30, events: 3, measures: 1 },
      last_received_at: new Date(Date.now() - 3 * 3600_000).toISOString(),
      first_day: '2026-08-01', raw_days: 30, rolled_up_days: 2,
      size: { raw_bytes: 812_000, aggregate_bytes: 1_450_000, total_bytes: 2_262_000 },
      unused_attributes: ['self_hosted'],
    }] })
    renderWithProviders(<UsageSection projectId={4} range={{ from: '2026-09-01', to: '2026-09-02' }} />)
    expect(await screen.findByText('34')).toBeInTheDocument()
    expect(screen.getByText('3 h ago')).toBeInTheDocument()
    expect(screen.getByText('2.3 MB')).toBeInTheDocument()
    expect(screen.getByText(/30 raw · 2 rolled up/)).toBeInTheDocument()
    expect(screen.getByText('self_hosted')).toBeInTheDocument()
    expect(endpoints.stats).toHaveBeenCalledWith({ project_id: 4, from: '2026-09-01', to: '2026-09-02' })
  })

  it('shows an empty project without errors', async () => {
    vi.spyOn(endpoints, 'stats').mockResolvedValue({ from: 'a', to: 'b', database_bytes: 0, projects: [{
      project_id: 5, series: [], totals: { views: 0, events: 0, measures: 0 }, last_received_at: null,
      first_day: null, raw_days: 0, rolled_up_days: 0, size: null, unused_attributes: [],
    }] })
    renderWithProviders(<UsageSection projectId={5} range={{ from: 'a', to: 'b' }} />)
    expect(await screen.findByText('Nothing received yet')).toBeInTheDocument()
    expect(screen.getByText('unknown')).toBeInTheDocument()
  })

  it('offers a retry when the stats fail', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(endpoints, 'stats').mockRejectedValue(new Error('boom'))
    renderWithProviders(<UsageSection projectId={4} range={{ from: 'a', to: 'b' }} />)
    await user.click(await screen.findByRole('button', { name: 'Retry' }))
    expect(spy).toHaveBeenCalledTimes(2)
  })
})
