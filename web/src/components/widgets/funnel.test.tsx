import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
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
  it('renders one row per step, in query order', () => {
    const { container } = renderFunnel({
      columns: ['step', 'value'],
      rows: [
        ['visit', '100'],
        ['signup', '50'],
        ['purchase', '25'],
      ],
      truncated: false,
    })
    const steps = container.querySelectorAll('[data-funnel-step]')
    expect(steps).toHaveLength(3)
    expect([...steps].map((s) => s.querySelector('span')?.textContent)).toEqual(['visit', 'signup', 'purchase'])
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

  it('says how much of the step before each step keeps', () => {
    const { container } = renderFunnel({
      columns: ['step', 'value'],
      rows: [
        ['visit', '200'],
        ['signup', '50'],
        ['purchase', '40'],
      ],
      truncated: false,
    })
    const scope = within(container)
    expect(scope.getByText('25% of the step before')).toBeInTheDocument()
    expect(scope.getByText('80% of the step before')).toBeInTheDocument()
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderFunnel({ columns: ['step', 'value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('shows the step value, conversion and drop-off on hover', async () => {
    const user = userEvent.setup()
    renderFunnel({
      columns: ['step', 'value'],
      rows: [
        ['visit', '20000'],
        ['signup', '5000'],
      ],
      truncated: false,
    })
    const signup = screen.getByText('signup')
    await user.hover(signup)
    const card = screen.getByRole('tooltip')
    expect(card).toHaveTextContent('signup')
    expect(card).toHaveTextContent('Value5,000')
    expect(card).toHaveTextContent('Of the first step25%')
    expect(card).toHaveTextContent('Of the step before25%')
    expect(card).toHaveTextContent('Dropped15,000')

    await user.unhover(signup)
    await user.hover(screen.getByText('visit'))
    expect(screen.getByRole('tooltip')).not.toHaveTextContent('step before')
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(8)
  })
})
