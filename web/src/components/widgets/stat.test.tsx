import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Stat, { contract } from './stat'

function renderStat(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 300, height: 300 }}>
      <Stat data={data} props={props} />
    </div>,
  )
}

describe('stat', () => {
  it('renders the value of the first row', () => {
    renderStat({ columns: ['value'], rows: [['12345']], truncated: false })
    expect(screen.getByText('12.3K')).toBeInTheDocument()
  })

  it('renders a delta badge when previous is present, colored and arrowed by sign', () => {
    renderStat({
      columns: ['value', 'previous'],
      rows: [['120', '100']],
      truncated: false,
    })
    expect(screen.getByText('+20%')).toBeInTheDocument()
  })

  it('renders a negative delta without a badge missing', () => {
    renderStat({
      columns: ['value', 'previous'],
      rows: [['80', '100']],
      truncated: false,
    })
    expect(screen.getByText('-20%')).toBeInTheDocument()
  })

  it('omits the delta badge when previous is absent (missing optional input)', () => {
    renderStat({ columns: ['value'], rows: [['5']], truncated: false })
    expect(screen.queryByText(/%/)).not.toBeInTheDocument()
  })

  it('draws a sparkline and aggregates the series when x is present', () => {
    const { container } = renderStat(
      {
        columns: ['x', 'value'],
        rows: [
          ['2026-01-01', '10'],
          ['2026-01-02', '20'],
        ],
        truncated: false,
      },
      { aggregate: 'sum' },
    )
    expect(screen.getByText('30')).toBeInTheDocument()
    expect(container.querySelector('svg')).toBeInTheDocument()
  })

  it('honors the aggregate prop', () => {
    renderStat(
      {
        columns: ['x', 'value'],
        rows: [
          ['2026-01-01', '10'],
          ['2026-01-02', '20'],
        ],
        truncated: false,
      },
      { aggregate: 'last' },
    )
    expect(screen.getByText('20')).toBeInTheDocument()
  })

  it('applies the format prop', () => {
    renderStat({ columns: ['value'], rows: [['0.5']], truncated: false }, { format: 'percent' })
    expect(screen.getByText('50%')).toBeInTheDocument()
  })

  it('marks an approximate value with ≈, and the change it makes', () => {
    renderStat({
      columns: ['value', 'previous'],
      rows: [['3500', '3000']],
      truncated: false,
      approximate: ['value'],
    })
    expect(screen.getByText('≈3,500')).toBeInTheDocument()
    expect(screen.getByText('≈+16.7%')).toBeInTheDocument()
  })

  it('marks the change when only previous is approximate', () => {
    renderStat({
      columns: ['value', 'previous'],
      rows: [['80', '100']],
      truncated: false,
      approximate: ['previous'],
    })
    expect(screen.getByText('80')).toBeInTheDocument()
    expect(screen.getByText('≈-20%')).toBeInTheDocument()
  })

  it('leaves an exact change unmarked', () => {
    renderStat({
      columns: ['value', 'previous'],
      rows: [['120', '100']],
      truncated: false,
      approximate: [],
    })
    expect(screen.getByText('+20%')).toBeInTheDocument()
  })

  it('marks the aggregate of an approximate series', () => {
    renderStat({
      columns: ['x', 'value'],
      rows: [
        ['2026-01-01', '10'],
        ['2026-01-02', '20'],
      ],
      truncated: false,
      approximate: ['value'],
    })
    expect(screen.getByText('≈30')).toBeInTheDocument()
  })

  it('leaves an exact value unmarked', () => {
    renderStat({
      columns: ['value', 'previous'],
      rows: [['5', '4']],
      truncated: false,
      approximate: ['previous'],
    })
    expect(screen.getByText('5')).toBeInTheDocument()
  })

  it('shows an empty value as a dash, never 0', () => {
    renderStat({
      columns: ['value', 'previous'],
      rows: [['', '100']],
      truncated: false,
      approximate: ['value'],
    })
    expect(screen.getByText('–')).toBeInTheDocument()
    expect(screen.queryByText('0')).not.toBeInTheDocument()
    expect(screen.queryByText(/%/)).not.toBeInTheDocument()
  })

  it('names the dash of an empty value for a screen reader', () => {
    renderStat({ columns: ['value'], rows: [['']], truncated: false })
    expect(screen.getByLabelText('no value')).toHaveTextContent('–')
  })

  it('leaves empty values out of a sparkline stat, never counting them as 0', () => {
    renderStat(
      {
        columns: ['x', 'value'],
        rows: [
          ['2026-01-01', '10'],
          ['2026-01-02', ''],
          ['2026-01-03', '20'],
        ],
        truncated: false,
      },
      { aggregate: 'avg' },
    )
    expect(screen.getByText('15')).toBeInTheDocument()
  })

  it('takes the last value of a sparkline stat, not a trailing empty one', () => {
    renderStat(
      {
        columns: ['x', 'value'],
        rows: [
          ['2026-01-01', '10'],
          ['2026-01-02', '20'],
          ['2026-01-03', ''],
        ],
        truncated: false,
      },
      { aggregate: 'last' },
    )
    expect(screen.getByText('20')).toBeInTheDocument()
  })

  it('shows a sparkline stat with only empty values as a dash, never 0', () => {
    const { container } = renderStat(
      {
        columns: ['x', 'value'],
        rows: [
          ['2026-01-01', ''],
          ['2026-01-02', ''],
        ],
        truncated: false,
      },
      { aggregate: 'sum' },
    )
    expect(screen.getByLabelText('no value')).toHaveTextContent('–')
    expect(screen.queryByText('0')).not.toBeInTheDocument()
    expect(container.querySelector('svg')).not.toBeInTheDocument()
  })

  it('still shows a zero value as 0', () => {
    renderStat({ columns: ['value'], rows: [['0']], truncated: false })
    expect(screen.getByText('0')).toBeInTheDocument()
    expect(screen.queryByText('–')).not.toBeInTheDocument()
  })

  it('shows no change line when previous is empty', () => {
    renderStat({
      columns: ['value', 'previous'],
      rows: [['120', '']],
      truncated: false,
    })
    expect(screen.getByText('120')).toBeInTheDocument()
    expect(screen.queryByText(/%/)).not.toBeInTheDocument()
    expect(screen.queryByText('vs previous')).not.toBeInTheDocument()
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderStat({
      columns: ['value'],
      rows: [],
      truncated: false,
    })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql', 'query'])
    expect(contract.defaultWidth).toBe(3)
    expect(contract.defaultHeight).toBe(3)
  })
})
