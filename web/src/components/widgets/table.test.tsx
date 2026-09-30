import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it } from 'vitest'
import type { SqlData, WidgetProps } from './types'
import Table, { contract } from './table'

function renderTable(data: SqlData, props: WidgetProps['props'] = {}, stateKey?: string) {
  return render(
    <div style={{ width: 400, height: 300 }}>
      <Table data={data} props={props} stateKey={stateKey} />
    </div>
  )
}

const pages: SqlData = {
  columns: ['page', 'visitors'],
  rows: [
    ['/b', '10'],
    ['/page10', '30'],
    ['/a', ''],
    ['/page2', '20'],
  ],
  truncated: false,
}

function column(container: HTMLElement, i: number): string[] {
  return [...container.querySelectorAll('tbody tr')].map((tr) => tr.children[i].textContent ?? '')
}

function header(name: string): HTMLElement {
  return screen.getByRole('columnheader', { name: new RegExp(name) })
}

afterEach(() => localStorage.clear())

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
    // row 1: page, visitors=0; row 2: page, visitors=100 (the top of the scale, capped at 45%)
    expect((cells[1] as HTMLElement).style.backgroundColor).toContain('0%')
    expect((cells[3] as HTMLElement).style.backgroundColor).toContain('45%')
    // an untouched column never gets a background shade.
    expect((cells[0] as HTMLElement).style.backgroundColor).toBe('')
  })

  it('renders nothing broken for an empty result', () => {
    const { container } = renderTable({ columns: ['a'], rows: [], truncated: false })
    expect(container.firstChild).toBeEmptyDOMElement()
  })

  it('sorts a number column descending, then ascending, then back to query order', async () => {
    const user = userEvent.setup()
    const { container } = renderTable(pages)
    const button = screen.getByRole('button', { name: /visitors/ })
    expect(header('visitors')).not.toHaveAttribute('aria-sort')

    await user.click(button)
    expect(column(container, 1)).toEqual(['30', '20', '10', ''])
    expect(header('visitors')).toHaveAttribute('aria-sort', 'descending')

    await user.click(button)
    // Empty cells stay last in both directions.
    expect(column(container, 1)).toEqual(['10', '20', '30', ''])
    expect(header('visitors')).toHaveAttribute('aria-sort', 'ascending')

    await user.click(button)
    expect(column(container, 0)).toEqual(['/b', '/page10', '/a', '/page2'])
    expect(header('visitors')).not.toHaveAttribute('aria-sort')
  })

  it('sorts a text column ascending first, with digits compared as numbers', async () => {
    const user = userEvent.setup()
    const { container } = renderTable(pages)
    await user.click(screen.getByRole('button', { name: /page/ }))
    expect(column(container, 0)).toEqual(['/a', '/b', '/page2', '/page10'])
    await user.click(screen.getByRole('button', { name: /page/ }))
    expect(column(container, 0)).toEqual(['/page10', '/page2', '/b', '/a'])
  })

  it('keeps query order between equal values', async () => {
    const user = userEvent.setup()
    const { container } = renderTable({
      columns: ['page', 'visitors'],
      rows: [
        ['/x', '5'],
        ['/y', '9'],
        ['/z', '5'],
      ],
      truncated: false,
    })
    await user.click(screen.getByRole('button', { name: /visitors/ }))
    expect(column(container, 0)).toEqual(['/y', '/x', '/z'])
    await user.click(screen.getByRole('button', { name: /visitors/ }))
    expect(column(container, 0)).toEqual(['/x', '/z', '/y'])
  })

  it('keeps each shaded cell its own shade when it moves', async () => {
    const user = userEvent.setup()
    const { container } = renderTable(
      { columns: ['page', 'visitors'], rows: [['/home', '0'], ['/about', '100']], truncated: false },
      { colorscale: ['visitors'] }
    )
    await user.click(screen.getByRole('button', { name: /visitors/ }))
    const cells = container.querySelectorAll('tbody td')
    expect(cells[1].textContent).toBe('100')
    expect((cells[1] as HTMLElement).style.backgroundColor).toContain('45%')
  })

  it('remembers the sort under its state key', async () => {
    const user = userEvent.setup()
    const first = renderTable(pages, {}, 'w')
    await user.click(screen.getByRole('button', { name: /visitors/ }))
    expect(JSON.parse(localStorage.getItem('w.sort')!)).toEqual({ column: 'visitors', dir: 'desc' })
    first.unmount()

    const { container } = renderTable(pages, {}, 'w')
    expect(column(container, 1)).toEqual(['30', '20', '10', ''])
  })

  it('ignores a remembered sort on a column the result no longer has', () => {
    localStorage.setItem('w.sort', JSON.stringify({ column: 'gone', dir: 'desc' }))
    const { container } = renderTable(pages, {}, 'w')
    expect(column(container, 0)).toEqual(['/b', '/page10', '/a', '/page2'])
    expect(localStorage.getItem('w.sort')).not.toBeNull()
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.inputs).toEqual({ open: true, columns: [] })
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(10)
  })
})
