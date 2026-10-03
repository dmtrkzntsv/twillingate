import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeAll, describe, expect, it, vi } from 'vitest'
import { emptyView, type Filter, type TableView } from '@/lib/table-view'
import { FilterBar, PageFooter } from './table-filters'

// jsdom lacks the pointer-capture and scrolling calls Radix Select makes when it opens.
beforeAll(() => {
  Element.prototype.hasPointerCapture ??= () => false
  Element.prototype.releasePointerCapture ??= () => {}
  Element.prototype.scrollIntoView ??= () => {}
})

const columns = ['Attribute', 'Day', 'Value', 'Count']
const numeric = new Set(['Count'])

type Option = { value: string; rows: number; capped: boolean }

function attributes(capped = false): Option[] {
  return [
    { value: '$os', rows: 12, capped },
    { value: 'plan', rows: 7, capped },
    { value: 'ref', rows: 3, capped },
  ]
}

function renderBar(view: TableView, opts: { options?: () => Promise<Option[]>; error?: string } = {}) {
  const onView = vi.fn<(v: TableView) => void>()
  const options = vi.fn(opts.options ?? (() => Promise.resolve(attributes())))
  const utils = render(
    <FilterBar columns={columns} numeric={numeric} view={view} onView={onView} options={options} error={opts.error} />
  )
  return { ...utils, onView, options }
}

const withFilters = (...filters: Filter[]): TableView => ({ ...emptyView, filters, offset: 2000 })

describe('FilterBar', () => {
  it('adds an in filter through the editor, back on the first page', async () => {
    const user = userEvent.setup()
    const { onView, options } = renderBar({ ...emptyView, offset: 1000 })
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    expect(screen.getByRole('combobox', { name: 'Column' })).toHaveTextContent('Attribute')
    expect(screen.getByRole('combobox', { name: 'Operator' })).toHaveTextContent('in')
    await user.click(await screen.findByRole('option', { name: /\$os/ }))
    await user.click(screen.getByRole('option', { name: /plan/ }))
    await user.click(screen.getByRole('button', { name: 'Apply' }))
    expect(options).toHaveBeenCalledWith('Attribute', [])
    expect(onView).toHaveBeenCalledWith({
      filters: [{ column: 'Attribute', op: 'in', value: ['$os', 'plan'] }],
      sort: null,
      offset: 0,
    })
  })

  it('shows each option with its row count', async () => {
    const user = userEvent.setup()
    renderBar(emptyView)
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    expect(await screen.findByRole('option', { name: /\$os/ })).toHaveTextContent('12')
  })

  it('takes a typed value for > in a text input', async () => {
    const user = userEvent.setup()
    const { onView } = renderBar(emptyView)
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    await user.click(screen.getByRole('combobox', { name: 'Column' }))
    await user.click(screen.getByRole('option', { name: 'Count' }))
    await user.click(screen.getByRole('combobox', { name: 'Operator' }))
    await user.click(screen.getByRole('option', { name: '>' }))
    await user.type(screen.getByRole('textbox', { name: 'Value' }), '100')
    await user.click(screen.getByRole('button', { name: 'Apply' }))
    expect(onView).toHaveBeenCalledWith({ filters: [{ column: 'Count', op: '>', value: '100' }], sort: null, offset: 0 })
  })

  it('edits a chip in place when it is clicked', async () => {
    const user = userEvent.setup()
    const { onView } = renderBar(withFilters({ column: 'Count', op: '>', value: '100' }))
    await user.click(screen.getByRole('button', { name: 'Count > 100' }))
    const input = screen.getByRole('textbox', { name: 'Value' })
    await user.clear(input)
    await user.type(input, '50')
    await user.click(screen.getByRole('button', { name: 'Apply' }))
    expect(onView).toHaveBeenCalledWith({ filters: [{ column: 'Count', op: '>', value: '50' }], sort: null, offset: 0 })
  })

  it('cuts a long value list to three and a count', () => {
    renderBar(withFilters({ column: 'Value', op: 'not in', value: ['a', 'b', 'c', 'd', 'e'] }))
    expect(screen.getByRole('button', { name: 'Value not in a, b, c +2' })).toBeInTheDocument()
  })

  it('greys a chip on a column the table does not have, saying why', async () => {
    const user = userEvent.setup()
    renderBar(withFilters({ column: 'gone', op: '=', value: 'x' }))
    const chip = screen.getByRole('button', { name: 'gone = x' })
    expect(chip.closest('[data-stale]')).not.toBeNull()
    await user.hover(chip)
    expect(await screen.findByRole('tooltip')).toHaveTextContent('not in this table')
  })

  it('removes only the chip whose × is clicked, and clears all with two or more', async () => {
    const user = userEvent.setup()
    const a: Filter = { column: 'Attribute', op: 'in', value: ['$os', 'plan'] }
    const b: Filter = { column: 'Count', op: '>', value: '100' }
    const { onView, rerender, options } = renderBar(withFilters(a, b))
    await user.click(screen.getByRole('button', { name: 'Remove filter Count > 100' }))
    expect(onView).toHaveBeenLastCalledWith({ filters: [a], sort: null, offset: 0 })
    await user.click(screen.getByRole('button', { name: 'Clear all' }))
    expect(onView).toHaveBeenLastCalledWith({ filters: [], sort: null, offset: 0 })

    rerender(<FilterBar columns={columns} numeric={numeric} view={withFilters(a)} onView={onView} options={options} />)
    expect(screen.queryByRole('button', { name: 'Clear all' })).toBeNull()
  })

  it('ends a capped option list with a note', async () => {
    const user = userEvent.setup()
    renderBar(emptyView, { options: () => Promise.resolve(attributes(true)) })
    await user.click(screen.getByRole('button', { name: 'Filter' }))
    expect(await screen.findByText('Showing the most frequent values')).toBeInTheDocument()
  })

  it('shows a refusal under the bar', () => {
    renderBar(emptyView, { error: 'no column "x"' })
    expect(screen.getByText('no column "x"')).toHaveClass('text-destructive', 'text-xs')
  })

  it('marks the chips of a refused view, but not one on a column the table lacks', () => {
    const view = withFilters(
      { column: 'Attribute', op: 'in', value: ['plan'] },
      { column: 'Platform', op: '=', value: 'web' }
    )
    const { rerender, onView, options } = renderBar(view, { error: 'filters: value too long' })
    expect(screen.getByRole('button', { name: 'Attribute in plan' })).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('button', { name: 'Platform = web' })).not.toHaveAttribute('aria-invalid')
    rerender(<FilterBar columns={columns} numeric={numeric} view={view} onView={onView} options={options} />)
    expect(screen.getByRole('button', { name: 'Attribute in plan' })).not.toHaveAttribute('aria-invalid')
  })
})

describe('PageFooter', () => {
  it('shows the range and moves to the next page', async () => {
    const user = userEvent.setup()
    const onOffset = vi.fn()
    render(<PageFooter offset={0} limit={1000} matched={5335} onOffset={onOffset} />)
    expect(screen.getByText('1–1,000 of 5,335')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Previous page' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Next page' }))
    expect(onOffset).toHaveBeenCalledWith(1000)
  })

  it('stops at the last page', () => {
    render(<PageFooter offset={5000} limit={1000} matched={5335} onOffset={() => {}} />)
    expect(screen.getByText('5,001–5,335 of 5,335')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Next page' })).toBeDisabled()
  })

  it('renders nothing for a single page without a note', () => {
    const { container } = render(<PageFooter offset={0} limit={1000} matched={1000} onOffset={() => {}} />)
    expect(container).toBeEmptyDOMElement()
  })

  it('keeps a note on a single page', () => {
    render(<PageFooter offset={0} limit={1000} matched={10} onOffset={() => {}} note="a note" />)
    expect(screen.getByText('a note')).toBeInTheDocument()
  })
})
