import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Treemap, { contract } from './treemap'

function renderTreemap(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 500, height: 300 }}>
      <Treemap data={data} props={props} />
    </div>
  )
}

describe('treemap', () => {
  it('renders one leaf rect per row, flat, when parent is absent (missing optional input)', () => {
    const { container } = renderTreemap({
      columns: ['label', 'value'],
      rows: [
        ['a', '10'],
        ['b', '20'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('[data-node-depth="1"]')).toHaveLength(2)
    expect(container.querySelector('[data-node-name="a"]')).not.toBeNull()
    expect(container.querySelector('[data-node-name="b"]')).not.toBeNull()
  })

  it('nests two levels when parent is present', () => {
    const { container } = renderTreemap({
      columns: ['label', 'value', 'parent'],
      rows: [
        ['a', '10', 'P1'],
        ['b', '20', 'P1'],
        ['c', '30', 'P2'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('[data-node-depth="1"]')).toHaveLength(2)
    expect(container.querySelectorAll('[data-node-depth="2"]')).toHaveLength(3)
    expect(container.querySelector('[data-node-name="P1"]')).not.toBeNull()
    expect(container.querySelector('[data-node-name="P2"]')).not.toBeNull()
    expect(container.querySelector('[data-node-name="c"]')).not.toBeNull()
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderTreemap({ columns: ['label', 'value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(8)
  })
})
