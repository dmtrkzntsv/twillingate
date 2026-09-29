import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Heatmap, { contract } from './heatmap'

function renderHeatmap(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 400, height: 400 }}>
      <Heatmap data={data} props={props} />
    </div>
  )
}

describe('heatmap', () => {
  it('lays out x columns and y rows in first-seen order, not sorted', () => {
    const { container } = renderHeatmap({
      columns: ['x', 'y', 'value'],
      rows: [
        ['day2', 'week2', '10'],
        ['day1', 'week2', '20'],
        ['day1', 'week1', '30'],
      ],
      truncated: false,
    })
    const headerCells = Array.from(container.querySelectorAll('[data-col-header]')).map(
      (n) => n.textContent
    )
    expect(headerCells).toEqual(['day2', 'day1'])
    const rowHeaders = Array.from(container.querySelectorAll('[data-row-header]')).map((n) => n.textContent)
    expect(rowHeaders).toEqual(['week2', 'week1'])
  })

  it('shades a cell by its value scaled between the min and max, via color-mix on --chart-1', () => {
    const { container } = renderHeatmap({
      columns: ['x', 'y', 'value'],
      rows: [
        ['a', 'r', '0'],
        ['b', 'r', '100'],
      ],
      truncated: false,
    })
    const cells = container.querySelectorAll('[data-cell]')
    expect((cells[0] as HTMLElement).style.backgroundColor).toContain('color-mix')
    expect((cells[0] as HTMLElement).style.backgroundColor).toContain(' 15%')
    expect((cells[1] as HTMLElement).style.backgroundColor).toContain('100%')
  })

  it('prints values in cells only when labels is true', () => {
    const withoutLabels = renderHeatmap({
      columns: ['x', 'y', 'value'],
      rows: [['a', 'r', '5']],
      truncated: false,
    })
    expect(withoutLabels.container.querySelector('[data-cell]')?.textContent).toBe('')

    const withLabels = renderHeatmap(
      { columns: ['x', 'y', 'value'], rows: [['a', 'r', '5']], truncated: false },
      { labels: true }
    )
    expect(withLabels.container.querySelector('[data-cell]')?.textContent).toBe('5')
  })

  it('shows the cell row, column and value on hover, not as a native title', async () => {
    const user = userEvent.setup()
    const { container } = renderHeatmap(
      { columns: ['x', 'y', 'value'], rows: [['a', 'r', '0.5']], truncated: false },
      { format: 'percent' }
    )
    const cell = container.querySelector('[data-cell]') as HTMLElement
    expect(cell.title).toBe('')
    await user.hover(cell)
    const card = screen.getByRole('tooltip')
    expect(card).toHaveTextContent('r, a')
    expect(card).toHaveTextContent('Value50%')
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderHeatmap({ columns: ['x', 'y', 'value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(10)
  })
})
