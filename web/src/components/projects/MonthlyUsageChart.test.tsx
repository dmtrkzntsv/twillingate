import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import { endpoints, type UsageDay } from '@/lib/api'
import { renderWithProviders } from '@/test/render'
import { monthKeys, monthsRange, utcToday } from '@/lib/usage-months'
import MonthlyUsageChart, { overText } from './MonthlyUsageChart'

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
    expect(container.querySelector('.over-label')).toBeNull()
  })

  it('draws the budget as a dashed line, stacks the weighted checks, and labels a month over it in its usual colours', async () => {
    answer([day(`${last}-02`, 50, 100), day(`${current}-01`, 5)], 40, 0.5)
    const container = await draw()
    const line = container.querySelector('.recharts-reference-line line')
    expect(line).not.toBeNull()
    expect(line).toHaveAttribute('stroke-dasharray', '4 4')
    expect(screen.getByText('Budget 40')).toBeInTheDocument()
    expect(screen.getByText('Checks')).toBeInTheDocument()
    // Last month: 50 views and 100 checks at 0.5 make 100, 60 over 40: labelled above its bar, the series keep their colours.
    const labels = container.querySelectorAll('.over-label')
    expect([...labels].map((l) => l.textContent)).toEqual(['+60 (150%)'])
    expect(labels[0]).toHaveAttribute('fill', 'var(--destructive)')
    expect(container.querySelector('.recharts-bar-rectangle path[fill="var(--destructive)"]')).toBeNull()
    expect(container.querySelector('.recharts-bar-rectangle path[fill="var(--chart-1)"]')).not.toBeNull()
  })

  it('leaves the checks out at a ratio of 0, budget or not', async () => {
    answer([day(`${current}-01`, 5, 100)], 40, 0)
    const container = await draw()
    expect(container.querySelector('.recharts-reference-line')).not.toBeNull()
    expect(screen.queryByText('Checks')).not.toBeInTheDocument()
  })

  it('says how far over in the tooltip\'s numbers', () => {
    expect(overText(5_200, 20_000)).toBe('+5,200 (26%)')
    expect(overText(12_345.4, 10_000)).toBe('+12.3K (123%)')
  })
})
