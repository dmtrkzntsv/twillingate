import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import { endpoints } from '@/lib/api'
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
    const countries = screen.getByRole('row', { name: /countries/ })
    expect(within(countries).getByText('no cap')).toBeInTheDocument()
    const users = screen.getByRole('row', { name: /users/ })
    expect(within(users).getByText('—')).toBeInTheDocument()
  })

  it('says when nothing in the range is capped or there is no data', async () => {
    vi.spyOn(endpoints, 'capUsage').mockResolvedValue({ project_id: 6, from: 'a', to: 'b', dimensions: [] })
    renderWithProviders(<CapImpactSection projectId={6} range={{ from: 'a', to: 'b' }} />)
    expect(await screen.findByText('No data in this range.')).toBeInTheDocument()
  })
})
