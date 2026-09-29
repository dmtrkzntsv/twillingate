import { Table as ShadcnTable, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatValue, type Format } from '@/lib/format'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface TableProps {
  formats?: Record<string, Format>
  colorscale?: string[]
}

export const contract: Contract = {
  description: 'Every column the query returns, in order; a raw drill-down table for a card that lists rows.',
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

function isNumericCell(v: string): boolean {
  return v === '' || (v.trim() !== '' && Number.isFinite(Number(v)))
}

export default function Table({ data, props }: WidgetProps<TableProps>) {
  const sql = data as SqlData
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

  return (
    <div className="h-full overflow-auto">
      <ShadcnTable>
        <TableHeader>
          <TableRow>
            {sql.columns.map((col) => (
              <TableHead
                key={col}
                className={`h-8 text-xs font-medium text-muted-foreground ${numericColumns.has(col) ? 'text-right' : ''}`}
              >
                {col}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {sql.rows.map((row, ri) => (
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
