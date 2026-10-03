import { useEffect, useState } from 'react'
import { ArrowDownIcon, ArrowUpIcon } from 'lucide-react'
import { FilterBar, PageFooter, type OptionLoader } from '@/components/table-filters'
import { Table as ShadcnTable, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatValue, type Format } from '@/lib/format'
import {
  applyView,
  distinctValues,
  emptyView,
  isDecimal,
  liveFilters,
  type Sort,
  type TableView,
} from '@/lib/table-view'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface TableProps {
  formats?: Record<string, Format>
  colorscale?: string[]
  mode?: 'local' | 'remote'
}

export const contract: Contract = {
  description:
    'Every column the query returns, in order; a raw drill-down table for a card that lists rows. Viewers sort it by clicking a header. Viewers filter it by column; with mode "remote" filters, sort and paging run on the server over the whole result.',
  accepts: ['sql'],
  inputs: { open: true, columns: [] },
  props: {
    type: 'object',
    properties: {
      formats: { type: 'object', additionalProperties: { enum: ['number', 'percent', 'duration'] } },
      colorscale: { type: 'array', items: { type: 'string' } },
      mode: { enum: ['local', 'remote'] },
    },
    additionalProperties: false,
  },
  defaultWidth: 6,
  defaultHeight: 10,
}

export const examples: Example[] = [
  {
    title: 'Top referrers',
    props: { formats: { visitors: 'number', bounce_rate: 'percent' }, colorscale: ['visitors'] },
    data: {
      columns: ['referrer', 'visitors', 'bounce_rate'],
      rows: [
        ['google.com', '620', '0.38'],
        ['(direct)', '410', '0.30'],
        ['bing.com', '180', '0.44'],
        ['github.com', '140', '0.27'],
        ['twitter.com', '95', '0.52'],
        ['duckduckgo.com', '60', '0.41'],
      ],
      truncated: false,
    },
  },
  {
    title: 'Filterable rows',
    props: { formats: { Count: 'number' } },
    data: {
      columns: ['Attribute', 'Day', 'Value', 'Count'],
      rows: [
        ['$os', '2026-09-30', 'iOS', '1240'],
        ['$os', '2026-09-30', 'Android', '860'],
        ['plan', '2026-09-30', 'pro', '410'],
        ['plan', '2026-09-30', 'free', '1690'],
        ['$os', '2026-09-29', 'iOS', '1180'],
        ['$os', '2026-09-29', 'Android', '905'],
        ['plan', '2026-09-29', 'pro', '395'],
        ['plan', '2026-09-29', 'free', '1720'],
      ],
      truncated: false,
    },
  },
]

const LOCAL_PAGE = 1000

export default function Table({
  data,
  props,
  view: controlled,
  onView,
  fetchDistinct,
  page,
  viewError,
  reloading,
}: WidgetProps<TableProps>) {
  const sql = data as SqlData
  // Without view and onView (the gallery), the table keeps its own, in memory.
  const [ownView, setOwnView] = useState<TableView>(emptyView)
  const [view, setView] = controlled !== undefined && onView !== undefined ? [controlled, onView] : [ownView, setOwnView]

  const remote = props.mode === 'remote'
  // A sort on a column the query no longer returns is kept but not applied: query order.
  const sort = view.sort && sql.columns.includes(view.sort.column) ? view.sort : null
  const local = remote ? null : applyView(sql.rows, sql.columns, { ...view, sort }, LOCAL_PAGE)

  // Rows can drop away under the page shown (a refresh, a refetch): move back
  // to the last page that has rows rather than show an empty one. A remote
  // answer counts only once it is the answer for this page.
  const shown = local
    ? { offset: view.offset, limit: LOCAL_PAGE, matched: local.matched }
    : page?.offset === view.offset
      ? page
      : undefined
  const pastEnd = shown !== undefined && shown.matched > 0 && shown.offset >= shown.matched
  useEffect(() => {
    if (pastEnd) setView({ ...view, offset: Math.floor((shown.matched - 1) / shown.limit) * shown.limit })
    // Only a new answer or page can end up past the end.
  }, [pastEnd, shown?.matched, shown?.offset])

  // An unfiltered empty result is the card's empty state; a filtered one keeps the bar.
  if (sql.rows.length === 0 && liveFilters(view, sql.columns).length === 0) return null

  const formats = props.formats ?? {}
  const colorscale = new Set(props.colorscale ?? [])

  const numericColumns = new Set(
    sql.columns.filter((_, i) => sql.rows.every((row) => (row[i] ?? '') === '' || isDecimal(row[i])))
  )

  const ranges = new Map<string, { min: number; max: number }>()
  sql.columns.forEach((col, i) => {
    if (!colorscale.has(col)) return
    const values = sql.rows
      .map((row) => row[i])
      .filter((v) => v !== '')
      .map(Number)
    if (values.length === 0) return
    ranges.set(col, { min: Math.min(...values), max: Math.max(...values) })
  })

  const rows = local ? local.rows : sql.rows
  const options: OptionLoader =
    remote && fetchDistinct
      ? fetchDistinct
      : (column, filters) =>
          Promise.resolve(distinctValues(sql.rows, sql.columns, column, filters).map((v) => ({ ...v, capped: false })))

  // Numbers read biggest first, text A to Z; the third click is query order again. A new sort starts on page one.
  const cycle = (col: string) => {
    const first: Sort['dir'] = numericColumns.has(col) ? 'desc' : 'asc'
    const next: Sort | null =
      sort?.column !== col
        ? { column: col, dir: first }
        : sort.dir === first
          ? { column: col, dir: first === 'asc' ? 'desc' : 'asc' }
          : null
    setView({ ...view, sort: next, offset: 0 })
  }

  return (
    <div className="flex h-full flex-col">
      <FilterBar
        columns={sql.columns}
        numeric={numericColumns}
        view={view}
        onView={setView}
        options={options}
        error={viewError}
      />
      {rows.length === 0 ? (
        <p className="py-6 text-center text-sm text-muted-foreground">No rows match these filters</p>
      ) : (
        <div className="min-h-0 flex-1 overflow-auto">
          <ShadcnTable>
            <TableHeader>
              <TableRow>
                {sql.columns.map((col) => {
                  const dir = sort?.column === col ? sort.dir : undefined
                  const Arrow = dir === 'asc' ? ArrowUpIcon : ArrowDownIcon
                  return (
                    <TableHead
                      key={col}
                      aria-sort={dir && (dir === 'asc' ? 'ascending' : 'descending')}
                      className={`h-8 text-xs font-medium text-muted-foreground ${numericColumns.has(col) ? 'text-right' : ''}`}
                    >
                      <button
                        type="button"
                        onClick={() => cycle(col)}
                        className={`inline-flex items-center gap-1 rounded-sm outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring ${
                          numericColumns.has(col) ? 'flex-row-reverse' : ''
                        } ${dir ? 'text-foreground' : ''}`}
                      >
                        {col}
                        <Arrow aria-hidden className={`size-3 ${dir ? '' : 'invisible'}`} />
                      </button>
                    </TableHead>
                  )
                })}
              </TableRow>
            </TableHeader>
            <TableBody className={`transition-opacity ${reloading ? 'opacity-60' : ''}`}>
              {rows.map((row, ri) => (
                <TableRow key={ri}>
                  {sql.columns.map((col, ci) => {
                    const raw = row[ci] ?? ''
                    const format = formats[col]
                    const text = format ? formatValue(raw === '' ? null : Number(raw), format) : raw
                    const range = ranges.get(col)
                    const style =
                      range && raw !== ''
                        ? {
                            backgroundColor: `color-mix(in oklab, var(--chart-1) ${
                              // Capped below full strength, so the cell's own text stays readable on it.
                              ((Number(raw) - range.min) / (range.max - range.min || 1)) * 45
                            }%, transparent)`,
                          }
                        : undefined
                    return (
                      <TableCell
                        key={col}
                        className={`py-1.5 ${numericColumns.has(col) ? 'text-right tabular-nums' : ''}`}
                        style={style}
                      >
                        {text}
                      </TableCell>
                    )
                  })}
                </TableRow>
              ))}
            </TableBody>
          </ShadcnTable>
        </div>
      )}
      {local ? (
        <PageFooter
          offset={view.offset}
          limit={LOCAL_PAGE}
          matched={local.matched}
          onOffset={(offset) => setView({ ...view, offset })}
          note={sql.truncated ? 'Filters apply to the loaded rows; this table needs mode "remote"' : undefined}
        />
      ) : (
        page && (
          <PageFooter
            offset={page.offset}
            limit={page.limit}
            matched={page.matched}
            onOffset={(offset) => setView({ ...view, offset })}
          />
        )
      )}
    </div>
  )
}
