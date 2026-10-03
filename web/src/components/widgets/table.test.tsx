import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { emptyView, type TableView } from '@/lib/table-view'
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

const attrs: SqlData = {
  columns: ['Attribute', 'Value', 'Count'],
  rows: [
    ['$os', 'iOS', '412'],
    ['$os', 'Android', '98'],
    ['plan', 'pro', '205'],
    ['plan', 'free', '150'],
    ['ref', 'news', '60'],
    ['ref', 'blog', '12'],
  ],
  truncated: false,
}

function view(...filters: TableView['filters']): TableView {
  return { ...emptyView, filters }
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

  it('sorts a text column ascending first, by code point as the server does', async () => {
    const user = userEvent.setup()
    const { container } = renderTable(pages)
    await user.click(screen.getByRole('button', { name: /page/ }))
    expect(column(container, 0)).toEqual(['/a', '/b', '/page10', '/page2'])
    await user.click(screen.getByRole('button', { name: /page/ }))
    expect(column(container, 0)).toEqual(['/page2', '/page10', '/b', '/a'])
  })

  it('puts v1.10 before v1.9 ascending: text compares by code point', async () => {
    const user = userEvent.setup()
    const { container } = renderTable({
      columns: ['version'],
      rows: [['v1.9'], ['v1.10']],
      truncated: false,
    })
    await user.click(screen.getByRole('button', { name: /version/ }))
    expect(column(container, 0)).toEqual(['v1.10', 'v1.9'])
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

  it('filters the loaded rows in local mode, offering their values without asking the server', async () => {
    const user = userEvent.setup()
    const fetchDistinct = vi.fn()
    const { container } = render(
      <Table data={attrs} props={{}} view={view({ column: 'Count', op: '>', value: '100' })} onView={() => {}} fetchDistinct={fetchDistinct} />
    )
    expect(column(container, 2)).toEqual(['412', '205', '150'])
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    // Counted among the rows the other filter keeps.
    expect(await screen.findByRole('option', { name: /plan/ })).toHaveTextContent('2')
    expect(screen.getByRole('option', { name: /\$os/ })).toHaveTextContent('1')
    expect(fetchDistinct).not.toHaveBeenCalled()
  })

  it('pages a local table past 1,000 matching rows', async () => {
    const user = userEvent.setup()
    const many: SqlData = { columns: ['n'], rows: Array.from({ length: 1500 }, (_, i) => [String(i)]), truncated: false }
    const onView = vi.fn()
    const { container } = render(<Table data={many} props={{}} view={emptyView} onView={onView} />)
    expect(container.querySelectorAll('tbody tr')).toHaveLength(1000)
    expect(screen.getByText('1–1,000 of 1,500')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Next page' }))
    expect(onView).toHaveBeenCalledWith({ ...emptyView, offset: 1000 })
  })

  it('says a truncated local table needs mode "remote"', () => {
    renderTable({ ...attrs, truncated: true })
    expect(screen.getByText(/needs mode "remote"/)).toBeInTheDocument()
  })

  it('keeps the bar when a filter matches nothing', () => {
    const { container } = render(
      <Table data={attrs} props={{}} view={view({ column: 'Count', op: '>', value: '100000' })} onView={() => {}} />
    )
    expect(screen.getByText('No rows match these filters')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Count > 100000' })).toBeInTheDocument()
    expect(container.querySelector('tbody tr')).toBeNull()
  })

  it('keeps the bar on a remote table the server filtered to nothing', () => {
    render(
      <Table
        data={{ ...attrs, rows: [] }}
        props={{ mode: 'remote' }}
        view={view({ column: 'Count', op: '>', value: '100000' })}
        onView={() => {}}
        page={{ offset: 0, limit: 1000, matched: 0, total: 6 }}
      />
    )
    expect(screen.getByText('No rows match these filters')).toBeInTheDocument()
  })

  it("renders a remote table's rows as given, with the footer from the page", () => {
    const { container } = render(
      <Table
        data={attrs}
        props={{ mode: 'remote' }}
        view={view({ column: 'Count', op: '>', value: '100' })}
        onView={() => {}}
        page={{ offset: 0, limit: 6, matched: 40, total: 90 }}
      />
    )
    expect(column(container, 2)).toEqual(['412', '98', '205', '150', '60', '12'])
    expect(screen.getByText('1–6 of 40')).toBeInTheDocument()
  })

  it("asks a remote table's loader for the picker's values", async () => {
    const user = userEvent.setup()
    const fetchDistinct = vi.fn().mockResolvedValue([{ value: 'plan', rows: 900, capped: false }])
    render(<Table data={attrs} props={{ mode: 'remote' }} view={emptyView} onView={() => {}} fetchDistinct={fetchDistinct} />)
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    expect(await screen.findByRole('option', { name: /plan/ })).toHaveTextContent('900')
    expect(fetchDistinct).toHaveBeenCalledWith('Attribute', [])
  })

  it('shows query order for a sort on a column the result no longer has', () => {
    const { container } = render(
      <Table data={pages} props={{}} view={{ ...emptyView, sort: { column: 'gone', dir: 'asc' } }} onView={() => {}} />
    )
    expect(column(container, 0)).toEqual(['/b', '/page10', '/a', '/page2'])
  })

  it('sorts through onView when controlled, back on the first page', async () => {
    const user = userEvent.setup()
    const onView = vi.fn()
    render(
      <Table
        data={pages}
        props={{ mode: 'remote' }}
        view={{ ...emptyView, offset: 1000 }}
        onView={onView}
        page={{ offset: 1000, limit: 4, matched: 2000, total: 2000 }}
      />
    )
    await user.click(screen.getByRole('button', { name: /visitors/ }))
    expect(onView).toHaveBeenCalledWith({ filters: [], sort: { column: 'visitors', dir: 'desc' }, offset: 0 })
  })

  it('moves back to the last page when a remote answer ends before the page shown', () => {
    const onView = vi.fn()
    render(
      <Table
        data={{ ...pages, rows: [] }}
        props={{ mode: 'remote' }}
        view={{ ...emptyView, offset: 3000 }}
        onView={onView}
        page={{ offset: 3000, limit: 1000, matched: 2500, total: 2500 }}
      />
    )
    expect(onView).toHaveBeenCalledWith({ ...emptyView, offset: 2000 })
  })

  it('moves back to the first page when the loaded rows no longer reach the page shown', () => {
    const onView = vi.fn()
    render(<Table data={pages} props={{}} view={{ ...emptyView, offset: 1000 }} onView={onView} />)
    expect(onView).toHaveBeenCalledWith({ ...emptyView, offset: 0 })
  })

  it('leaves a remote page alone while the answer on screen is for another one', () => {
    const onView = vi.fn()
    render(
      <Table
        data={pages}
        props={{ mode: 'remote' }}
        view={{ ...emptyView, offset: 3000 }}
        onView={onView}
        page={{ offset: 0, limit: 1000, matched: 2500, total: 2500 }}
        reloading
      />
    )
    expect(onView).not.toHaveBeenCalled()
  })

  it('dims the rows while a later load is in flight', () => {
    const { container } = render(<Table data={pages} props={{}} reloading />)
    expect(container.querySelector('tbody')).toHaveClass('opacity-60')
  })

  it('exposes its contract', () => {
    expect(contract.accepts).toEqual(['sql'])
    expect(contract.inputs).toEqual({ open: true, columns: [] })
    expect(contract.defaultWidth).toBe(6)
    expect(contract.defaultHeight).toBe(10)
  })
})
