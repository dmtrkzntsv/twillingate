import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Combo, { contract } from './combo'

function renderCombo(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 600, height: 400 }}>
      <Combo data={data} props={props} />
    </div>
  )
}

const data: SqlData = {
  columns: ['x', 'bar', 'line'],
  rows: [
    ['2026-01-01', '100', '0.2'],
    ['2026-01-02', '150', '0.3'],
  ],
  truncated: false,
}

describe('combo', () => {
  it('renders a bar series and a line series for the same x', () => {
    const { container } = renderCombo(data)
    expect(container.querySelectorAll('.recharts-bar-rectangle')).toHaveLength(2)
    expect(container.querySelector('.recharts-line')).toBeInTheDocument()
  })

  it('formats each axis with its own format prop', () => {
    renderCombo(data, { bar_format: 'number', line_format: 'percent' })
    expect(screen.getByText('30%')).toBeInTheDocument()
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderCombo({ columns: ['x', 'bar', 'line'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(8)
  })
})
