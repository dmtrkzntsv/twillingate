import { ArrowDownIcon, ArrowUpIcon } from 'lucide-react'
import { Table as ShadcnTable, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useStoredState } from '@/hooks/use-stored-state'
import { formatValue, type Format } from '@/lib/format'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface TableProps {
  formats?: Record<string, Format>
  colorscale?: string[]
}

export const contract: Contract = {
  description:
    'Every column the query returns, in order; a raw drill-down table for a card that lists rows. Viewers sort it by clicking a header.',
  accepts: ['sql'],
  inputs: { open: true, columns: [] },
  props: {
    type: 'object',
    properties: {
      formats: { type: 'object', additionalProperties: { enum: ['number', 'percent', 'duration'] } },
      colorscale: { type: 'array', items: { type: 'string' } },
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
]

interface Sort {
  column: string
  dir: 'asc' | 'desc'
}

function isNumericCell(v: string): boolean {
  return v === '' || (v.trim() !== '' && Number.isFinite(Number(v)))
}

function parseSort(v: unknown): Sort | null {
  if (typeof v !== 'object' || v === null) return null
  const { column, dir } = v as Record<string, unknown>
  return typeof column === 'string' && (dir === 'asc' || dir === 'desc') ? { column, dir } : null
}

const collator = new Intl.Collator(undefined, { numeric: true })

/** The rows ordered by one column, empty cells last either way; ties keep query order. */
function sortRows(rows: string[][], index: number, numeric: boolean, dir: Sort['dir']): string[][] {
  const sign = dir === 'asc' ? 1 : -1
  return [...rows].sort((a, b) => {
    const x = a[index] ?? ''
    const y = b[index] ?? ''
    if (x === '' || y === '') return (x === '' ? 1 : 0) - (y === '' ? 1 : 0)
    return sign * (numeric ? Number(x) - Number(y) : collator.compare(x, y))
  })
}

export default function Table({ data, props, stateKey }: WidgetProps<TableProps>) {
  const sql = data as SqlData
  const [stored, setSort] = useStoredState(stateKey && `${stateKey}.sort`, parseSort)
  if (sql.rows.length === 0) return null

  const formats = props.formats ?? {}
  const colorscale = new Set(props.colorscale ?? [])

  const numericColumns = new Set(
    sql.columns.filter((_, i) => sql.rows.every((row) => isNumericCell(row[i] ?? '')))
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

  // A remembered sort on a column the query no longer returns is left alone, not cleared.
  const sort = stored && sql.columns.includes(stored.column) ? stored : null
  const rows = sort
    ? sortRows(sql.rows, sql.columns.indexOf(sort.column), numericColumns.has(sort.column), sort.dir)
    : sql.rows

  // Numbers read biggest first, text A to Z; the third click is query order again.
  const cycle = (col: string) => {
    const first: Sort['dir'] = numericColumns.has(col) ? 'desc' : 'asc'
    if (sort?.column !== col) setSort({ column: col, dir: first })
    else if (sort.dir === first) setSort({ column: col, dir: first === 'asc' ? 'desc' : 'asc' })
    else setSort(null)
  }

  return (
    <div className="h-full overflow-auto">
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
        <TableBody>
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
  )
}
