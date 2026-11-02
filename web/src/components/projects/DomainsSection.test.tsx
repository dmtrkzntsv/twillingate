import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import { endpoints, type Monitors } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import DomainsSection from './DomainsSection'

/** The `YYYY-MM-DD` n days after today in UTC. */
function utcDay(n: number): string {
  const now = new Date()
  return new Date(Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate() + n)).toISOString().slice(0, 10)
}

function show(domains: Monitors['domains']) {
  vi.spyOn(endpoints, 'monitors').mockResolvedValue({ interval_seconds: 60, timeout_seconds: 5, paused: false, monitors: [], domains })
  return renderWithProviders(<DomainsSection projectId={4} />)
}

beforeEach(() => vi.restoreAllMocks())

describe('DomainsSection', () => {
  it('lists the domains soonest renewal first, unknown last, with days left and the sites', async () => {
    show([
      { domain: 'unknown.test', origins: ['https://unknown.test'], expires_at: '', checked_at: '' },
      { domain: 'late.test', origins: ['https://a.late.test', 'https://b.late.test'], expires_at: utcDay(300), checked_at: 'x' },
      { domain: 'soon.test', origins: ['https://soon.test'], expires_at: utcDay(10), checked_at: 'x' },
    ])
    const region = await screen.findByRole('region', { name: 'Domains' })
    const names = within(region).getAllByRole('row').slice(1).map((r) => r.getAttribute('aria-label'))
    expect(names).toEqual(['soon.test', 'late.test', 'unknown.test'])
    const soon = within(within(region).getByRole('row', { name: 'soon.test' }))
    expect(soon.getByText(utcDay(10))).toBeInTheDocument()
    expect(soon.getByText('10 days')).toBeInTheDocument()
    expect(soon.getByText('1 site')).toBeInTheDocument()
    expect(within(region).getByText('2 sites')).toBeInTheDocument()
    expect(within(region).getByText('unknown')).toBeInTheDocument()
  })

  it('reads an expired domain as expired, in the destructive colour', async () => {
    show([{ domain: 'gone.test', origins: ['https://gone.test'], expires_at: utcDay(-2), checked_at: 'x' }])
    expect(await screen.findByText('expired')).toHaveClass('text-destructive')
  })

  it('is absent with no readings', async () => {
    const read = show([])
    await vi.waitFor(() => expect(endpoints.monitors).toHaveBeenCalled())
    await read.client.fetchQuery({ queryKey: ['monitor', 4], queryFn: () => endpoints.monitors(4) })
    expect(screen.queryByRole('region', { name: 'Domains' })).not.toBeInTheDocument()
  })
})
