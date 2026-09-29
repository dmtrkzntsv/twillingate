import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import BarList, { contract } from './bar_list'

function renderBarList(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 300, height: 300 }}>
      <BarList data={data} props={props} />
    </div>
  )
}

describe('bar_list', () => {
  it('renders a ranked row per result, with its formatted value', () => {
    renderBarList({
      columns: ['label', 'value'],
      rows: [
        ['/home', '12345'],
        ['/about', '50'],
      ],
      truncated: false,
    })
    expect(screen.getByText('/home')).toBeInTheDocument()
    expect(screen.getByText('12.3K')).toBeInTheDocument()
    expect(screen.getByText('/about')).toBeInTheDocument()
    expect(screen.getByText('50')).toBeInTheDocument()
  })

  it('sizes the inline bar relative to the largest value', () => {
    const { container } = renderBarList({
      columns: ['label', 'value'],
      rows: [
        ['/home', '100'],
        ['/about', '25'],
      ],
      truncated: false,
    })
    const bars = container.querySelectorAll('[data-bar-fill]')
    expect((bars[0] as HTMLElement).style.width).toBe('100%')
    expect((bars[1] as HTMLElement).style.width).toBe('25%')
  })

  it('applies the format prop', () => {
    renderBarList({ columns: ['label', 'value'], rows: [['a', '0.5']], truncated: false }, { format: 'percent' })
    expect(screen.getByText('50%')).toBeInTheDocument()
  })

  it('shows the full label, exact value, share and rank on hover', async () => {
    const user = userEvent.setup()
    renderBarList({
      columns: ['label', 'value'],
      rows: [
        ['/home', '12345'],
        ['/about', '4115'],
      ],
      truncated: false,
    })
    const row = screen.getByText('/home')
    await user.hover(row)
    const card = screen.getByRole('tooltip')
    expect(card).toHaveTextContent('/home')
    expect(card).toHaveTextContent('12,345')
    expect(card).toHaveTextContent('Share of list75%')
    expect(card).toHaveTextContent('Rank1 of 2')

    await user.unhover(row)
    expect(screen.queryByRole('tooltip')).not.toBeInTheDocument()
  })

  it('leaves the share out of a cut list, or one that is not counts', async () => {
    const user = userEvent.setup()
    const cut = renderBarList({ columns: ['label', 'value'], rows: [['a', '5']], truncated: true })
    await user.hover(screen.getByText('a'))
    expect(screen.getByRole('tooltip')).not.toHaveTextContent('Share')
    cut.unmount()

    renderBarList({ columns: ['label', 'value'], rows: [['b', '0.5']], truncated: false }, { format: 'percent' })
    await user.hover(screen.getByText('b'))
    expect(screen.getByRole('tooltip')).not.toHaveTextContent('Share')
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderBarList({ columns: ['label', 'value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(8)
  })
})
