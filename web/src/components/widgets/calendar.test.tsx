import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { CardMode } from '@/components/share/card-mode'
import type { SqlData, WidgetProps } from './types'
import Calendar, { contract } from './calendar'

function renderCalendar(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 900, height: 200 }}>
      <Calendar data={data} props={props} />
    </div>
  )
}

describe('calendar', () => {
  it('lays out the last 53 weeks ending at the max day, as 7-cell columns', () => {
    const { container } = renderCalendar({
      columns: ['day', 'value'],
      rows: [
        ['2024-06-10', '1'],
        ['2024-06-15', '2'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('[data-day]')).toHaveLength(53 * 7)
    expect(container.querySelector('[data-day="2024-06-15"]')).not.toBeNull()
  })

  it('shades a day by its value scaled between the min and max, via color-mix on --chart-1', () => {
    const { container } = renderCalendar({
      columns: ['day', 'value'],
      rows: [
        ['2024-06-01', '0'],
        ['2024-06-15', '100'],
      ],
      truncated: false,
    })
    const low = container.querySelector('[data-day="2024-06-01"]') as HTMLElement
    const high = container.querySelector('[data-day="2024-06-15"]') as HTMLElement
    expect(low.style.fill).toContain('color-mix')
    expect(low.style.fill).toContain(' 15%')
    expect(high.style.fill).toContain('100%')
  })

  it('labels the columns that contain the first of a month', () => {
    const { container } = renderCalendar({
      columns: ['day', 'value'],
      rows: [['2024-06-15', '1']],
      truncated: false,
    })
    const labels = Array.from(container.querySelectorAll('[data-month-label]'))
      .map((n) => n.textContent)
      .filter((t) => t)
    // 53 weeks is a little over a year: 12 or 13 month boundaries, depending
    // on where the grid happens to start relative to the 1st of the month.
    expect(labels.length).toBeGreaterThanOrEqual(12)
    expect(labels.length).toBeLessThanOrEqual(13)
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderCalendar({ columns: ['day', 'value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('shows the day and its exact value on hover, or that it has no data', async () => {
    const user = userEvent.setup()
    const { container } = renderCalendar({
      columns: ['day', 'value'],
      rows: [['2024-06-15', '12345']],
      truncated: false,
    })
    expect(container.querySelector('title')).toBeNull()

    await user.hover(container.querySelector('[data-day="2024-06-15"]') as Element)
    expect(screen.getByRole('tooltip')).toHaveTextContent('Jun 15, 2024')
    expect(screen.getByRole('tooltip')).toHaveTextContent('Value12,345')

    await user.hover(container.querySelector('[data-day="2024-06-14"]') as Element)
    expect(screen.getByRole('tooltip')).toHaveTextContent('Jun 14, 2024')
    expect(screen.getByRole('tooltip')).toHaveTextContent('No data')
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(12)
    expect(contract.defaultHeight).toBe(4)
  })

  it('on a share card, draws only the weeks the data reaches, at least 13', () => {
    const days = (n: number, end: string) =>
      Array.from({ length: n }, (_, i) => [new Date(Date.parse(`${end}T00:00:00Z`) - i * 86400000).toISOString().slice(0, 10), '5'])
    const weeks = (rows: string[][]) => {
      const { container, unmount } = render(
        <CardMode.Provider value={true}>
          <Calendar data={{ columns: ['day', 'value'], rows, truncated: false }} props={{}} />
        </CardMode.Provider>
      )
      const n = container.querySelectorAll('rect[data-day]').length / 7
      unmount()
      return n
    }
    // 2026-09-27 is a Sunday: 120 days reach back into the 18th week.
    expect(weeks(days(120, '2026-09-27'))).toBe(18)
    expect(weeks(days(10, '2026-09-27'))).toBe(13)
    expect(weeks(days(400, '2026-09-27'))).toBe(53)
  })
})
