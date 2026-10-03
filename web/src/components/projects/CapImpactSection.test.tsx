import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { ApiError, endpoints } from '@/lib/api'
import { TooltipProvider } from '@/components/ui/tooltip'
import { renderWithProviders } from '@/test/render'
import CapImpactSection from './CapImpactSection'

beforeEach(() => vi.restoreAllMocks())

describe('CapImpactSection', () => {
  it('puts capped dimensions first and shows no cap for 0', async () => {
    vi.spyOn(endpoints, 'capUsage').mockResolvedValue({ project_id: 6, from: 'a', to: 'b', dimensions: [
      { setting: 'VIEWS_DIMENSIONS_TOP_N', dimension: 'countries', cap: 0, max_values_per_day: 38, max_day: '2026-09-02', days: 30, days_capped: 0, folded_share: 0 },
      { setting: 'VIEWS_DIMENSIONS_TOP_N', dimension: 'paths', cap: 1000, max_values_per_day: 1001, max_day: '2026-09-11', days: 30, days_capped: 4, folded_share: 0.41 },
      { setting: 'IDENTITIES_TOP_N', dimension: 'users', cap: 1000, max_values_per_day: 33, max_day: '2026-09-03', days: 27, days_capped: 0, folded_share: null },
    ] })
    renderWithProviders(<CapImpactSection projectId={6} range={{ from: 'a', to: 'b' }} />)
    const rows = await screen.findAllByRole('row')
    expect(within(rows[1]).getByText('paths')).toBeInTheDocument()
    expect(within(rows[1]).getByText('4 of 30')).toBeInTheDocument()
    expect(within(rows[1]).getByText('41%')).toBeInTheDocument()
    expect(within(rows[1]).getByText('capped')).toBeInTheDocument()
    const countries = screen.getByRole('row', { name: /countries/ })
    expect(within(countries).getByText('no cap')).toBeInTheDocument()
    expect(within(countries).queryByText('capped')).not.toBeInTheDocument()
    const users = screen.getByRole('row', { name: /users/ })
    expect(within(users).getByText('—')).toBeInTheDocument()
  })

  it('says when nothing in the range is capped or there is no data', async () => {
    vi.spyOn(endpoints, 'capUsage').mockResolvedValue({ project_id: 6, from: 'a', to: 'b', dimensions: [] })
    renderWithProviders(<CapImpactSection projectId={6} range={{ from: 'a', to: 'b' }} />)
    expect(await screen.findByText('No data in this range.')).toBeInTheDocument()
  })

  it('shows the server refusal and retries', async () => {
    const user = userEvent.setup()
    const spy = vi.spyOn(endpoints, 'capUsage').mockRejectedValue(new ApiError(400, 'range over 400 days; narrow the date range'))
    renderWithProviders(<CapImpactSection projectId={6} range={{ from: 'a', to: 'b' }} />)
    expect(await screen.findByText('range over 400 days; narrow the date range')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Retry' }))
    expect(spy).toHaveBeenCalledTimes(2)
  })

  it('shows a skeleton first, then keeps the previous rows while a new range loads', async () => {
    const mk = (dimension: string) => ({ project_id: 6, from: 'a', to: 'b', dimensions: [
      { setting: 'VIEWS_DIMENSIONS_TOP_N' as const, dimension, cap: 10, max_values_per_day: 5, max_day: '2026-09-01', days: 3, days_capped: 0, folded_share: 0 },
    ] })
    let release!: () => void
    const second = new Promise<ReturnType<typeof mk>>((res) => { release = () => res(mk('browsers')) })
    vi.spyOn(endpoints, 'capUsage').mockResolvedValueOnce(mk('paths')).mockReturnValueOnce(second)
    const { client, rerender } = renderWithProviders(<CapImpactSection projectId={6} range={{ from: 'a', to: 'b' }} />)
    const region = screen.getByRole('region', { name: 'Cap impact' })
    expect(region.querySelector('[data-slot="skeleton"]')).not.toBeNull()
    expect(screen.queryByText(/Couldn't load/)).not.toBeInTheDocument()
    expect(await screen.findByText('paths')).toBeInTheDocument()
    rerender(<QueryClientProvider client={client}><TooltipProvider><CapImpactSection projectId={6} range={{ from: 'c', to: 'd' }} /></TooltipProvider></QueryClientProvider>)
    await waitFor(() => expect(endpoints.capUsage).toHaveBeenCalledTimes(2))
    expect(screen.getByText('paths')).toBeInTheDocument()
    release()
    expect(await screen.findByText('browsers')).toBeInTheDocument()
  })
})
