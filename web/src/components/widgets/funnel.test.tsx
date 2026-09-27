import { render, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Funnel, { contract } from './funnel'

function renderFunnel(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 400, height: 400 }}>
      <Funnel data={data} props={props} />
    </div>
  )
}

describe('funnel', () => {
  it('renders one trapezoid per step, in query order', () => {
    const { container } = renderFunnel({
      columns: ['step', 'value'],
      rows: [
        ['visit', '100'],
        ['signup', '50'],
        ['purchase', '25'],
      ],
      truncated: false,
    })
    expect(container.querySelectorAll('.recharts-funnel-trapezoid')).toHaveLength(3)
  })

  it("labels each step with its share of the first step's value", () => {
    const { container } = renderFunnel({
      columns: ['step', 'value'],
      rows: [
        ['visit', '100'],
        ['signup', '50'],
        ['purchase', '25'],
      ],
      truncated: false,
    })
    const scope = within(container)
    expect(scope.getByText('visit')).toBeInTheDocument()
    expect(scope.getByText('100%')).toBeInTheDocument()
    expect(scope.getByText('signup')).toBeInTheDocument()
    expect(scope.getByText('50%')).toBeInTheDocument()
    expect(scope.getByText('purchase')).toBeInTheDocument()
    expect(scope.getByText('25%')).toBeInTheDocument()
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderFunnel({ columns: ['step', 'value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(8)
  })
})
