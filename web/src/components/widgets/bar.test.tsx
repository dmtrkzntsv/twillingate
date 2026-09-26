import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Bar, { contract } from './bar'

function renderBar(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 600, height: 400 }}>
      <Bar data={data} props={props} />
    </div>
  )
}

const twoCategories: SqlData = {
  columns: ['x', 'y'],
  rows: [
    ['a', '1'],
    ['b', '2'],
  ],
  truncated: false,
}

describe('bar', () => {
  it('renders one bar series when series is absent (missing optional input)', () => {
    const { container } = renderBar(twoCategories)
    expect(container.querySelectorAll('.recharts-bar-rectangle')).toHaveLength(2)
  })

  it('renders one bar per series value', () => {
    const { container } = renderBar({
      columns: ['x', 'y', 'series'],
      rows: [
        ['a', '1', 'p'],
        ['a', '2', 'q'],
        ['b', '3', 'p'],
        ['b', '4', 'q'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('.recharts-bar-rectangle')).toHaveLength(4)
  })

  it('lays out the category axis vertically when horizontal is set', () => {
    const { container } = renderBar(twoCategories, { horizontal: true })
    expect(container.querySelector('.recharts-yAxis')?.classList.contains('yAxis')).toBe(true)
    // the category axis (x data) is now the y-axis tick labels
    expect(container.textContent).toContain('a')
  })

  it('changes rectangle geometry when stacked is set', () => {
    const data: SqlData = {
      columns: ['x', 'y', 'series'],
      rows: [
        ['a', '1', 'p'],
        ['a', '2', 'q'],
        ['b', '3', 'p'],
        ['b', '4', 'q'],
      ],
      truncated: false,
    }
    const unstacked = renderBar(data)
    const stacked = renderBar(data, { stacked: true })
    const rectAt = (c: HTMLElement, i: number) => c.querySelectorAll('.recharts-bar-rectangle path')[i]?.getAttribute('d')
    expect(rectAt(stacked.container, 1)).not.toEqual(rectAt(unstacked.container, 1))
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderBar({ columns: ['x', 'y'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(8)
  })
})
