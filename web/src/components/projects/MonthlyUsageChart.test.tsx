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

function answer(series: UsageDay[], ratio?: number) {
  vi.spyOn(endpoints, 'usage').mockResolvedValue({
    from: monthsRange(today).from, to: today, database_bytes: 0, database_series: [],
    projects: [{ project_id: 4, series, totals: { views: 0, events: 0, measures: 0, checks: 0 }, last_received_at: null, first_day: null, size: null }],
    quota_check_ratio: ratio,
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

  it('marks the current month "so far", and stacks the checks at their weight, with no budget line', async () => {
    answer([day(`${last}-02`, 50, 100), day(`${current}-01`, 5)], 0.5)
    const container = await draw()
    expect(screen.getByText('so far')).toBeInTheDocument()
    expect(screen.getByText('Checks')).toBeInTheDocument()
    expect(container.querySelector('.recharts-reference-line')).toBeNull()
    expect(container.querySelector('.recharts-bar-rectangle path[fill="var(--destructive)"]')).toBeNull()
    // Every series in its own colour, the checks the fourth.
    expect(container.querySelector('.recharts-bar-rectangle path[fill="var(--chart-4)"]')).not.toBeNull()
  })

  it('counts a check as one event by default', async () => {
    answer([day(`${current}-01`, 5, 100)])
    await draw()
    expect(screen.getByText('Checks')).toBeInTheDocument()
  })

  it('leaves the checks out at a ratio of 0', async () => {
    answer([day(`${current}-01`, 5, 100)], 0)
    await draw()
    expect(screen.queryByText('Checks')).not.toBeInTheDocument()
  })
})
