import { render, screen } from '@testing-library/react'
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
