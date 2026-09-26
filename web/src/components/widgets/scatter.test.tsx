import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Scatter, { contract } from './scatter'

function renderScatter(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 400, height: 400 }}>
      <Scatter data={data} props={props} />
    </div>
  )
}

describe('scatter', () => {
  it('renders one group of points when series is absent (missing optional input)', () => {
    const { container } = renderScatter({
      columns: ['x', 'y'],
      rows: [
        ['1', '2'],
        ['3', '4'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('.recharts-scatter-symbol')).toHaveLength(2)
  })

  it('renders one group of points per series value', () => {
    const { container } = renderScatter({
      columns: ['x', 'y', 'series'],
      rows: [
        ['1', '2', 'a'],
        ['3', '4', 'b'],
      ],
      truncated: false,
    })
    // both groups still render one point each; check both scatter series exist
    expect(container.querySelectorAll('.recharts-scatter')).toHaveLength(2)
  })

  it('scales point size when size is present (missing optional input otherwise)', () => {
    const { container } = renderScatter({
      columns: ['x', 'y', 'size'],
      rows: [
        ['1', '2', '1'],
        ['3', '4', '100'],
      ],
      truncated: false,
    })
    const symbols = container.querySelectorAll('.recharts-scatter-symbol path')
    expect(symbols[0]?.getAttribute('d')).not.toEqual(symbols[1]?.getAttribute('d'))
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderScatter({ columns: ['x', 'y'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(8)
  })
})
