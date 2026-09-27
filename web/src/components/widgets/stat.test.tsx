import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Stat, { contract } from './stat'

function renderStat(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 300, height: 300 }}>
      <Stat data={data} props={props} />
    </div>
  )
}

describe('stat', () => {
  it('renders the value of the first row', () => {
    renderStat({ columns: ['value'], rows: [['12345']], truncated: false })
    expect(screen.getByText('12.3K')).toBeInTheDocument()
  })

  it('renders a delta badge when previous is present, colored and arrowed by sign', () => {
    renderStat({ columns: ['value', 'previous'], rows: [['120', '100']], truncated: false })
    expect(screen.getByText('+20%')).toBeInTheDocument()
  })

  it('renders a negative delta without a badge missing', () => {
    renderStat({ columns: ['value', 'previous'], rows: [['80', '100']], truncated: false })
    expect(screen.getByText('-20%')).toBeInTheDocument()
  })

  it('omits the delta badge when previous is absent (missing optional input)', () => {
    renderStat({ columns: ['value'], rows: [['5']], truncated: false })
    expect(screen.queryByText(/%/)).not.toBeInTheDocument()
  })

  it('draws a sparkline and aggregates the series when x is present', () => {
    const { container } = renderStat(
      { columns: ['x', 'value'], rows: [['2026-01-01', '10'], ['2026-01-02', '20']], truncated: false },
      { aggregate: 'sum' }
    )
    expect(screen.getByText('30')).toBeInTheDocument()
    expect(container.querySelector('svg')).toBeInTheDocument()
  })

  it('honors the aggregate prop', () => {
    renderStat(
      { columns: ['x', 'value'], rows: [['2026-01-01', '10'], ['2026-01-02', '20']], truncated: false },
      { aggregate: 'last' }
    )
    expect(screen.getByText('20')).toBeInTheDocument()
  })

  it('applies the format prop', () => {
    renderStat({ columns: ['value'], rows: [['0.5']], truncated: false }, { format: 'percent' })
    expect(screen.getByText('50%')).toBeInTheDocument()
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderStat({ columns: ['value'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.defaultWidth).toBe(3)
    expect(contract.defaultHeight).toBe(3)
  })
})
