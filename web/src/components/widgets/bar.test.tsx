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

  it('moves the category ticks to the y-axis when horizontal is set', () => {
    const vertical = renderBar(twoCategories)
    const horizontal = renderBar(twoCategories, { horizontal: true })
    const categoryTicks = (c: HTMLElement, axis: 'x' | 'y') =>
      Array.from(c.querySelectorAll(`.recharts-${axis}Axis-tick-labels tspan`)).map((n) => n.textContent)

    // by default, the category (x data) sits on the x-axis, the numeric
    // value on the y-axis
    expect(categoryTicks(vertical.container, 'x')).toEqual(['a', 'b'])
    expect(categoryTicks(vertical.container, 'y')).not.toContain('a')

    // horizontal swaps that: the category moves to the y-axis
    expect(categoryTicks(horizontal.container, 'y')).toEqual(['a', 'b'])
    expect(categoryTicks(horizontal.container, 'x')).not.toContain('a')
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
