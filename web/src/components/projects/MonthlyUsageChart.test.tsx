import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { endpoints, type UsageDay } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import { monthKeys, monthsRange, utcToday } from '@/lib/usage-months'
import MonthlyUsageChart from './MonthlyUsageChart'

beforeEach(() => vi.restoreAllMocks())

const today = utcToday()
const [oldest, , , , last, current] = monthKeys(today)
const day = (d: string, views: number, checks = 0): UsageDay => ({ day: d, views, events: 0, measures: 0, checks, total_bytes: null })

function answer(series: UsageDay[], budget?: number | null, ratio?: number) {
  vi.spyOn(endpoints, 'usage').mockResolvedValue({
    from: monthsRange(today).from, to: today, database_bytes: 0, database_series: [],
    projects: [{ project_id: 4, series, totals: { views: 0, events: 0, measures: 0, checks: 0 }, last_received_at: null, first_day: null, size: null }],
    events_monthly_budget: budget, quota_check_ratio: ratio,
  })
}

async function draw() {
  const view = renderWithProviders(<MonthlyUsageChart projectId={4} />)
  await waitFor(() => expect(view.container.querySelector('.recharts-bar-rectangle')).not.toBeNull())
  return view.container
}

describe('MonthlyUsageChart', () => {
  it('reads one range, from the 1st of the month five months back through today', async () => {
    answer([day(`${current}-01`, 5)])
    await draw()
    expect(endpoints.usage).toHaveBeenCalledWith({ project_id: 4, from: `${oldest}-01`, to: today })
  })

  it('marks the current month "so far", and has no budget line or checks without a budget', async () => {
    answer([day(`${last}-02`, 50, 900), day(`${current}-01`, 5, 900)], null, 1)
    const container = await draw()
    expect(screen.getByText('so far')).toBeInTheDocument()
    expect(container.querySelector('.recharts-reference-line')).toBeNull()
    expect(screen.queryByText('Checks')).not.toBeInTheDocument()
    expect(container.querySelector('.recharts-bar-rectangle path[fill="var(--destructive)"]')).toBeNull()
  })

  it('draws the budget as a dashed line, stacks the weighted checks, and marks a month over it', async () => {
    answer([day(`${last}-02`, 50, 100), day(`${current}-01`, 5)], 40, 0.5)
    const container = await draw()
    const line = container.querySelector('.recharts-reference-line line')
    expect(line).not.toBeNull()
    expect(line).toHaveAttribute('stroke-dasharray', '4 4')
    expect(screen.getByText('Budget 40')).toBeInTheDocument()
    expect(screen.getByText('Checks')).toBeInTheDocument()
    // Last month: 50 views and 100 checks at 0.5, over 40, in the warning colour on every family.
    expect(container.querySelectorAll('.recharts-bar-rectangle path[fill="var(--destructive)"]').length).toBeGreaterThanOrEqual(2)
  })

  it('leaves the checks out at a ratio of 0, budget or not', async () => {
    answer([day(`${current}-01`, 5, 100)], 40, 0)
    const container = await draw()
    expect(container.querySelector('.recharts-reference-line')).not.toBeNull()
    expect(screen.queryByText('Checks')).not.toBeInTheDocument()
  })
})
