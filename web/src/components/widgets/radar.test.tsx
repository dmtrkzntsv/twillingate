import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Radar, { contract } from './radar'

function renderRadar(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 400, height: 400 }}>
      <Radar data={data} props={props} />
    </div>
  )
}

describe('radar', () => {
  it('renders one shape when series is absent (missing optional input)', () => {
    const { container } = renderRadar({
      columns: ['axis', 'value'],
      rows: [
        ['speed', '3'],
        ['range', '5'],
        ['comfort', '4'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('.recharts-radar-polygon')).toHaveLength(1)
  })

  it('renders one shape per series value', () => {
    const { container } = renderRadar({
      columns: ['axis', 'value', 'series'],
      rows: [
        ['speed', '3', 'a'],
        ['range', '5', 'a'],
        ['speed', '2', 'b'],
        ['range', '4', 'b'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('.recharts-radar-polygon')).toHaveLength(2)
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderRadar({ columns: ['axis', 'value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(4)
    expect(contract.defaultHeight).toBe(8)
  })
})
