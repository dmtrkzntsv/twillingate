import { ArrowDownIcon, ArrowUpIcon } from 'lucide-react'
import { Line, LineChart, ResponsiveContainer } from 'recharts'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import type { Contract, SqlData, WidgetProps } from './types'

interface StatProps {
  format?: Format
  aggregate?: 'sum' | 'last' | 'avg'
}

export const contract: Contract = {
  description:
    "A single number, optionally compared to a previous period; give `x` for a small trend line under it, and it becomes the series' aggregate.",
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'value', types: ['number'] },
      { name: 'previous', types: ['number'], optional: true },
      { name: 'x', types: ['day'], optional: true },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
      aggregate: { enum: ['sum', 'last', 'avg'] },
    },
    additionalProperties: false,
  },
  defaultWidth: 3,
  defaultHeight: 3,
}

function aggregate(values: number[], how: StatProps['aggregate']): number {
  if (values.length === 0) return 0
  if (how === 'last') return values[values.length - 1]
  const sum = values.reduce((a, b) => a + b, 0)
  return how === 'avg' ? sum / values.length : sum
}

export default function Stat({ data, props }: WidgetProps<StatProps>) {
  const sql = data as SqlData
  const format = props.format ?? 'number'
  const records = toRecords(sql, contract)
  if (records.length === 0) return null

  if (sql.columns.includes('x')) {
    const values = records.map((r) => Number(r.value ?? 0))
    const value = aggregate(values, props.aggregate ?? 'sum')
    const series = records.map((r, i) => ({ x: String(r.x ?? i), value: Number(r.value ?? 0) }))
    return (
      <div className="flex h-full flex-col justify-between gap-2 py-2">
        <span className="text-3xl font-semibold tracking-tight tabular-nums">{formatValue(value, format)}</span>
        <div className="h-10 w-full">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={series}>
              <Line
                type="monotone"
                dataKey="value"
                stroke="var(--chart-1)"
                strokeWidth={2}
                dot={false}
                isAnimationActive={false}
              />
            </LineChart>
          </ResponsiveContainer>
        </div>
      </div>
    )
  }

  const value = Number(records[0].value ?? 0)
  const previous = records[0].previous
  const delta = typeof previous === 'number' && previous !== 0 ? (value - previous) / previous : null

  return (
    <div className="flex h-full flex-col items-start justify-center gap-2 py-2">
      <span className="text-3xl font-semibold tracking-tight tabular-nums">{formatValue(value, format)}</span>
      {delta !== null && (
        <span
          className={
            delta >= 0
              ? 'inline-flex items-center gap-1 rounded-full bg-emerald-100 px-2 py-0.5 text-xs font-medium text-emerald-700 dark:bg-emerald-950 dark:text-emerald-400'
              : 'inline-flex items-center gap-1 rounded-full bg-red-100 px-2 py-0.5 text-xs font-medium text-red-700 dark:bg-red-950 dark:text-red-400'
          }
        >
          {delta >= 0 ? <ArrowUpIcon className="size-3" /> : <ArrowDownIcon className="size-3" />}
          {delta >= 0 ? '+' : ''}
          {formatValue(delta, 'percent')}
        </span>
      )}
    </div>
  )
}
