import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Area, { contract } from './area'

function renderArea(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 600, height: 400 }}>
      <Area data={data} props={props} />
    </div>
  )
}

describe('area', () => {
  it('renders one area when series is absent (missing optional input)', () => {
    const { container } = renderArea({
      columns: ['x', 'y'],
      rows: [
        ['2026-01-01', '1'],
        ['2026-01-02', '2'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('.recharts-area')).toHaveLength(1)
  })

  it('renders one area per series value', () => {
    const { container } = renderArea({
      columns: ['x', 'y', 'series'],
      rows: [
        ['2026-01-01', '1', 'a'],
        ['2026-01-01', '2', 'b'],
        ['2026-01-02', '3', 'a'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('.recharts-area')).toHaveLength(2)
  })

  it('changes stacking when stacked is set', () => {
    const data: SqlData = {
      columns: ['x', 'y', 'series'],
      rows: [
        ['2026-01-01', '1', 'a'],
        ['2026-01-01', '2', 'b'],
        ['2026-01-02', '3', 'a'],
        ['2026-01-02', '4', 'b'],
      ],
      truncated: false,
    }
    const unstacked = renderArea(data)
    const stacked = renderArea(data, { stacked: true })
    const path = (c: HTMLElement) => c.querySelectorAll('path.recharts-area-area')[1]?.getAttribute('d')
    expect(path(stacked.container)).not.toEqual(path(unstacked.container))
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderArea({ columns: ['x', 'y'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(8)
  })
})
