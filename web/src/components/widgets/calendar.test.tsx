import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
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
    expect(low.style.backgroundColor).toContain('color-mix')
    expect(low.style.backgroundColor).toContain('0%')
    expect(high.style.backgroundColor).toContain('100%')
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

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(12)
    expect(contract.defaultHeight).toBe(4)
  })
})
