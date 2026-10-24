import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Line, { contract } from './line'

function renderLine(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 600, height: 400 }}>
      <Line data={data} props={props} />
    </div>
  )
}

describe('line', () => {
  it('renders one line when series is absent (missing optional input)', () => {
    const { container } = renderLine({
      columns: ['x', 'y'],
      rows: [
        ['2026-01-01', '1'],
        ['2026-01-02', '2'],
      ],
      truncated: false,
    })
    expect(container.querySelector('svg')).toBeInTheDocument()
    expect(container.querySelectorAll('.recharts-line')).toHaveLength(1)
  })

  it('renders one line per series value', () => {
    const { container } = renderLine({
      columns: ['x', 'y', 'series'],
      rows: [
        ['2026-01-01', '1', 'a'],
        ['2026-01-01', '2', 'b'],
        ['2026-01-02', '3', 'a'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('.recharts-line')).toHaveLength(2)
  })

  it('changes the curve prop', () => {
    const data: SqlData = {
      columns: ['x', 'y'],
      rows: [
        ['2026-01-01', '1'],
        ['2026-01-02', '2'],
      ],
      truncated: false,
    }
    const stepped = renderLine(data, { curve: 'step' })
    const linear = renderLine(data, { curve: 'linear' })
    expect(stepped.container.querySelector('path.recharts-curve')?.getAttribute('d')).not.toEqual(
      linear.container.querySelector('path.recharts-curve')?.getAttribute('d')
    )
  })

  it('plots a numeric x to scale, in order', () => {
    // A retention curve renamed {offset: x, retention: y}, rows out of order.
    const { container } = renderLine({
      columns: ['x', 'y'],
      rows: [
        ['30', '0.2'],
        ['0', '1'],
        ['1', '0.6'],
        ['7', '0.4'],
      ],
      truncated: false,
    })
    const ticks = [...container.querySelectorAll('.recharts-xAxis-tick-labels tspan')].map((t) => t.textContent)
    expect(ticks.length).toBeGreaterThan(1)
    expect(ticks[0]).toBe('0')
    expect(ticks[ticks.length - 1]).toBe('30')
    const values = ticks.map(Number)
    expect(values.every((v, i) => Number.isInteger(v) && (i === 0 || v > values[i - 1]))).toBe(true)
  })

  it('keeps days on a category axis when x mixes days and numbers', () => {
    const { container } = renderLine({
      columns: ['x', 'y'],
      rows: [
        ['2026-01-02', '1'],
        ['3', '2'],
      ],
      truncated: false,
    })
    const ticks = [...container.querySelectorAll('.recharts-xAxis-tick-labels tspan')].map((t) => t.textContent)
    expect(ticks).toEqual(['Jan 2', '3'])
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderLine({ columns: ['x', 'y'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql', 'query'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(8)
  })
})
