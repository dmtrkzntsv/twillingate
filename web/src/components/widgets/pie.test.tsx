import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Pie, { contract } from './pie'

function renderPie(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 400, height: 400 }}>
      <Pie data={data} props={props} />
    </div>
  )
}

const slices: SqlData = {
  columns: ['label', 'value'],
  rows: [
    ['a', '30'],
    ['b', '70'],
  ],
  truncated: false,
}

describe('pie', () => {
  it('renders one slice per row', () => {
    const { container } = renderPie(slices)
    expect(container.querySelectorAll('.recharts-pie-sector')).toHaveLength(2)
  })

  it('opens a hole in the middle when donut is set', () => {
    const pie = renderPie(slices)
    const donut = renderPie(slices, { donut: true })
    const innerRadius = (c: HTMLElement) =>
      c.querySelector('.recharts-pie-sector path')?.getAttribute('d')
    expect(innerRadius(donut.container)).not.toEqual(innerRadius(pie.container))
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderPie({ columns: ['label', 'value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(4)
    expect(contract.defaultHeight).toBe(8)
  })
})
