import { useId } from 'react'
import { ArrowDownRightIcon, ArrowUpRightIcon } from 'lucide-react'
import { Area, AreaChart, ResponsiveContainer, YAxis } from 'recharts'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import type { Contract, Example, SqlData, WidgetProps } from './types'

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

export const examples: Example[] = [
  {
    title: 'Visitors',
    props: { format: 'number', aggregate: 'sum' },
    data: {
      columns: ['value', 'x'],
      rows: [
        ['240', '2026-09-01'],
        ['255', '2026-09-02'],
        ['260', '2026-09-03'],
        ['275', '2026-09-04'],
        ['300', '2026-09-05'],
        ['290', '2026-09-06'],
        ['310', '2026-09-07'],
        ['320', '2026-09-08'],
        ['335', '2026-09-09'],
        ['340', '2026-09-10'],
        ['355', '2026-09-11'],
        ['360', '2026-09-12'],
        ['375', '2026-09-13'],
        ['390', '2026-09-14'],
      ],
      truncated: false,
    },
  },
  {
    title: 'Bounce rate',
    props: { format: 'percent' },
    data: { columns: ['value', 'previous'], rows: [['0.42', '0.47']], truncated: false },
  },
]

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
  // An SVG id, so only characters url(#...) takes as they are.
  const fade = 'spark' + useId().replace(/[^a-zA-Z0-9_-]/g, '')
  if (records.length === 0) return null

  if (sql.columns.includes('x')) {
    const values = records.map((r) => Number(r.value ?? 0))
    const value = aggregate(values, props.aggregate ?? 'sum')
    const series = records.map((r, i) => ({ x: String(r.x ?? i), value: Number(r.value ?? 0) }))
    return (
      <div className="flex h-full flex-col justify-between gap-1 pt-1">
        <Figure>{formatValue(value, format)}</Figure>
        <div className="-mx-1 min-h-8 flex-1">
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={series} margin={{ top: 4, right: 4, bottom: 2, left: 4 }}>
              <defs>
                <linearGradient id={fade} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="var(--chart-1)" stopOpacity={0.28} />
                  <stop offset="100%" stopColor="var(--chart-1)" stopOpacity={0} />
                </linearGradient>
              </defs>
              {/* Its own range, not from zero: the sparkline is for the shape of the trend. */}
              <YAxis hide domain={['dataMin', 'dataMax']} />
              <Area
                type="monotone"
                baseValue="dataMin"
                dataKey="value"
                stroke="var(--chart-1)"
                strokeWidth={2}
                fill={`url(#${fade})`}
                // Only the latest point gets a dot: where the number above stands now.
                dot={(p: { index: number; cx?: number; cy?: number }) =>
                  p.index === series.length - 1 && p.cx !== undefined && p.cy !== undefined ? (
                    <circle
                      key="last"
                      cx={p.cx}
                      cy={p.cy}
                      r={3}
                      fill="var(--chart-1)"
                      stroke="var(--card)"
                      strokeWidth={2}
                    />
                  ) : (
                    <g key={p.index} />
                  )
                }
                isAnimationActive={false}
              />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      </div>
    )
  }

  const value = Number(records[0].value ?? 0)
  const previous = records[0].previous
  const delta = typeof previous === 'number' && previous !== 0 ? (value - previous) / previous : null

  return (
    <div className="flex h-full flex-col items-start justify-center gap-2 pt-1">
      <Figure>{formatValue(value, format)}</Figure>
      {delta !== null && (
        <div className="flex items-center gap-1.5 text-xs">
          <span
            className={
              delta >= 0
                ? 'inline-flex items-center gap-0.5 rounded-full bg-emerald-500/12 px-1.5 py-0.5 font-medium text-emerald-700 dark:text-emerald-400'
                : 'inline-flex items-center gap-0.5 rounded-full bg-red-500/12 px-1.5 py-0.5 font-medium text-red-700 dark:text-red-400'
            }
          >
            {delta >= 0 ? <ArrowUpRightIcon className="size-3" /> : <ArrowDownRightIcon className="size-3" />}
            {delta >= 0 ? '+' : ''}
            {formatValue(delta, 'percent')}
          </span>
          <span className="text-muted-foreground">vs previous</span>
        </div>
      )}
    </div>
  )
}

/** The headline number: large, proportional figures, never the series color. */
function Figure({ children }: { children: string }) {
  return <span className="text-3xl leading-none font-semibold tracking-tight">{children}</span>
}
