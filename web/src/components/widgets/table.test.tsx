import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Table, { contract } from './table'

function renderTable(data: SqlData, props: WidgetProps['props'] = {}) {
  return render(
    <div style={{ width: 400, height: 300 }}>
      <Table data={data} props={props} />
    </div>
  )
}

describe('table', () => {
  it('renders every column, in query order, with a header per column', () => {
    const { getAllByRole } = renderTable({
      columns: ['page', 'visitors', 'bounce_rate'],
      rows: [['/home', '120', '0.4']],
      truncated: false,
    })
    const headers = getAllByRole('columnheader').map((h) => h.textContent)
    expect(headers).toEqual(['page', 'visitors', 'bounce_rate'])
  })

  it('right-aligns numeric columns and leaves text columns left-aligned', () => {
    const { container } = renderTable({
      columns: ['page', 'visitors'],
      rows: [['/home', '120']],
      truncated: false,
    })
    const cells = container.querySelectorAll('tbody td')
    expect((cells[0] as HTMLElement).className).not.toContain('text-right')
    expect((cells[1] as HTMLElement).className).toContain('text-right')
  })

  it('applies formats to the named column only', () => {
    const { container } = renderTable(
      {
        columns: ['page', 'bounce_rate', 'raw_id'],
        rows: [['/home', '0.4', '12345']],
        truncated: false,
      },
      { formats: { bounce_rate: 'percent' } }
    )
    const cells = container.querySelectorAll('tbody td')
    expect(cells[1].textContent).toBe('40%')
    // raw_id is numeric but has no format: shown as-is, not compacted.
    expect(cells[2].textContent).toBe('12345')
  })

  it('shades colorscale columns by value, via color-mix on --chart-1', () => {
    const { container } = renderTable(
      {
        columns: ['page', 'visitors'],
        rows: [
          ['/home', '0'],
          ['/about', '100'],
        ],
        truncated: false,
      },
      { colorscale: ['visitors'] }
    )
    const cells = container.querySelectorAll('tbody td')
    // row 1: page, visitors=0; row 2: page, visitors=100
    expect((cells[1] as HTMLElement).style.backgroundColor).toContain('0%')
    expect((cells[3] as HTMLElement).style.backgroundColor).toContain('100%')
    // an untouched column never gets a background shade.
    expect((cells[0] as HTMLElement).style.backgroundColor).toBe('')
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderTable({ columns: ['a'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.inputs).toEqual({ open: true, columns: [] })
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(10)
  })
})
