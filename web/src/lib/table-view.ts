// Filters, sorts and pages table rows by the same rules the server applies in
// SQL (internal/shared/readsql), so a table filtered in the browser and one
// filtered by the server agree. internal/reporting/testdata/table-filters.json
// holds the cases both sides must pass.

export type FilterOp = '=' | '!=' | '<' | '>' | 'in' | 'not in'
export interface Filter {
  column: string
  op: FilterOp
  value: string | string[]
}
export interface Sort {
  column: string
  dir: 'asc' | 'desc'
}
export interface TableView {
  filters: Filter[]
  sort: Sort | null
  offset: number
}

export const emptyView: TableView = { filters: [], sort: null, offset: 0 }

/** The operators in the order the filter menu lists them. */
export const OPS: FilterOp[] = ['=', '!=', '<', '>', 'in', 'not in']

// Same pattern as readsql.IsDecimal: no spaces, no plus sign, no bare dot.
const DECIMAL = /^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$/

export function isDecimal(cell: string): boolean {
  return DECIMAL.test(cell)
}

/** The cell as a number when it is a finite decimal, else null. */
export function cellNumber(cell: string): number | null {
  if (!isDecimal(cell)) return null
  const n = Number(cell)
  return Number.isFinite(n) ? n : null
}

/**
 * Orders text by code point. SQLite's BINARY collation over UTF-8 does, and
 * comparing UTF-16 units would put an astral character before U+E000 to U+FFFF.
 */
export function compareText(a: string, b: string): number {
  const ia = a[Symbol.iterator]()
  const ib = b[Symbol.iterator]()
  for (;;) {
    const x = ia.next()
    const y = ib.next()
    if (x.done || y.done) return x.done === y.done ? 0 : x.done ? -1 : 1
    const cx = x.value.codePointAt(0)!
    const cy = y.value.codePointAt(0)!
    if (cx !== cy) return cx < cy ? -1 : 1
  }
}

function values(f: Filter): string[] {
  return Array.isArray(f.value) ? f.value : [f.value]
}

/**
 * Whether a row passes one filter. An empty cell reaches only `!=` and
 * `not in`; a filter on a column the result lacks passes every row.
 */
export function matches(row: string[], columns: string[], f: Filter): boolean {
  const i = columns.indexOf(f.column)
  if (i < 0) return true
  const cell = row[i] ?? ''
  const vs = values(f)
  switch (f.op) {
    case '=':
      return cell !== '' && cell === vs[0]
    case '!=':
      return cell === '' || cell !== vs[0]
    case 'in':
      return cell !== '' && vs.includes(cell)
    case 'not in':
      return cell === '' || !vs.includes(cell)
    case '<':
    case '>': {
      const sign = f.op === '<' ? -1 : 1
      if (isDecimal(vs[0])) {
        // Out of range reads as an infinity, which still orders correctly.
        const n = cellNumber(cell)
        return n !== null && (n < Number(vs[0]) ? -1 : n > Number(vs[0]) ? 1 : 0) === sign
      }
      return cell !== '' && compareText(cell, vs[0]) === sign
    }
  }
}

// Empty cells last, numbers before text, numbers by value, text by code point;
// the direction applies to the last two only, as in the server's ORDER BY.
function compareCells(a: string, b: string, dir: 1 | -1): number {
  if ((a === '') !== (b === '')) return a === '' ? 1 : -1
  if (a === '') return 0
  const na = cellNumber(a)
  const nb = cellNumber(b)
  if ((na === null) !== (nb === null)) return na === null ? 1 : -1
  if (na !== null && nb !== null && na !== nb) return (na < nb ? -1 : 1) * dir
  return compareText(a, b) * dir
}

/**
 * Orders two rows by the sort, then by every column ascending so pages never
 * overlap or skip. Without a sort it returns 0: the rows keep their order.
 */
export function compareRows(a: string[], b: string[], columns: string[], sort: Sort | null): number {
  if (!sort) return 0
  const i = columns.indexOf(sort.column)
  if (i >= 0) {
    const c = compareCells(a[i] ?? '', b[i] ?? '', sort.dir === 'desc' ? -1 : 1)
    if (c !== 0) return c
  }
  for (let j = 0; j < columns.length; j++) {
    const c = compareCells(a[j] ?? '', b[j] ?? '', 1)
    if (c !== 0) return c
  }
  return 0
}

/** The view's filters that name a column the result has. */
export function liveFilters(view: TableView, columns: string[]): Filter[] {
  return view.filters.filter((f) => columns.includes(f.column))
}

/**
 * Filters, sorts and pages the rows. `matched` counts rows after the filters
 * and `total` before them, whatever the page holds.
 */
export function applyView(
  rows: string[][],
  columns: string[],
  view: TableView,
  limit: number,
): { rows: string[][]; matched: number; total: number } {
  const filters = liveFilters(view, columns)
  const kept = rows.filter((r) => filters.every((f) => matches(r, columns, f)))
  // Array.prototype.sort is stable, so a null sort (comparing equal) keeps query order.
  const sorted = view.sort ? [...kept].sort((a, b) => compareRows(a, b, columns, view.sort)) : kept
  return { rows: sorted.slice(view.offset, view.offset + limit), matched: kept.length, total: rows.length }
}

/**
 * The values of one column with their row counts, among the rows the other
 * filters keep (so the picker still offers what a filter on this column would
 * add), most frequent first, then by the sort rule on the value.
 */
export function distinctValues(
  rows: string[][],
  columns: string[],
  column: string,
  filters: Filter[],
): { value: string; rows: number }[] {
  const i = columns.indexOf(column)
  if (i < 0) return []
  const others = filters.filter((f) => f.column !== column)
  const counts = new Map<string, number>()
  for (const r of rows) {
    if (!others.every((f) => matches(r, columns, f))) continue
    const cell = r[i] ?? ''
    counts.set(cell, (counts.get(cell) ?? 0) + 1)
  }
  return [...counts]
    .map(([value, n]) => ({ value, rows: n }))
    .sort((a, b) => b.rows - a.rows || compareCells(a.value, b.value, 1))
}

function isStrings(v: unknown): v is string[] {
  return Array.isArray(v) && v.every((s) => typeof s === 'string')
}

function parseFilter(raw: unknown): Filter | null {
  if (typeof raw !== 'object' || raw === null) return null
  const { column, op, value } = raw as Record<string, unknown>
  if (typeof column !== 'string' || !OPS.includes(op as FilterOp)) return null
  if (op === 'in' || op === 'not in') {
    return isStrings(value) && value.length > 0 ? { column, op, value } : null
  }
  return typeof value === 'string' ? { column, op: op as FilterOp, value } : null
}

/**
 * Reads a view saved in the browser's storage, dropping what no longer fits.
 * The offset is never kept: a reload starts on the first page.
 */
export function parseView(stored: unknown): TableView | null {
  if (typeof stored !== 'object' || stored === null || Array.isArray(stored)) return null
  const { filters, sort } = stored as Record<string, unknown>
  const s = typeof sort === 'object' && sort !== null ? (sort as Record<string, unknown>) : null
  return {
    filters: Array.isArray(filters) ? filters.flatMap((f) => parseFilter(f) ?? []) : [],
    sort: s && typeof s.column === 'string' && (s.dir === 'asc' || s.dir === 'desc') ? { column: s.column, dir: s.dir } : null,
    offset: 0,
  }
}
