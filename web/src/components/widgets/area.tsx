import { useId } from 'react'
import { Area as RechartsArea, AreaChart, CartesianGrid, XAxis, YAxis } from 'recharts'
import {
  ChartContainer,
  ChartTooltip,
} from '@/components/ui/chart'
import { activeDot, axis, formatTick, grid, niceTicks, seriesColor, seriesConfig, stackTotals, valueAxis, valuesOf } from '@/lib/chart'
import { formatValue, type Format } from '@/lib/format'
import { pivot, toRecords } from '@/lib/records'
import { legend, tooltip } from '@/components/chart-parts'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface AreaProps {
  format?: Format
  curve?: 'linear' | 'monotone' | 'step'
  stacked?: boolean
}

export const contract: Contract = {
  description:
    'A trend over time or category, filled under the line; use `stacked` to show parts of a whole changing over time.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'x', types: ['day', 'text'] },
      { name: 'y', types: ['number'] },
      { name: 'series', types: ['text'], optional: true },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
      curve: { enum: ['linear', 'monotone', 'step'] },
      stacked: { type: 'boolean' },
    },
    additionalProperties: false,
  },
  defaultWidth: 6,
  defaultHeight: 8,
}

// 7 days, so 3 series stays under the fixture row cap (14 days would be 42 rows).
export const examples: Example[] = [
  {
    title: 'Views per day by platform',
    props: { stacked: true },
    data: {
      columns: ['x', 'series', 'y'],
      rows: [
        ['2026-09-01', 'desktop', '420'],
        ['2026-09-01', 'mobile', '260'],
        ['2026-09-01', 'tablet', '60'],
        ['2026-09-02', 'desktop', '440'],
        ['2026-09-02', 'mobile', '270'],
        ['2026-09-02', 'tablet', '58'],
        ['2026-09-03', 'desktop', '430'],
        ['2026-09-03', 'mobile', '280'],
        ['2026-09-03', 'tablet', '62'],
        ['2026-09-04', 'desktop', '460'],
        ['2026-09-04', 'mobile', '300'],
        ['2026-09-04', 'tablet', '65'],
        ['2026-09-05', 'desktop', '480'],
        ['2026-09-05', 'mobile', '320'],
        ['2026-09-05', 'tablet', '70'],
        ['2026-09-06', 'desktop', '410'],
        ['2026-09-06', 'mobile', '290'],
        ['2026-09-06', 'tablet', '64'],
        ['2026-09-07', 'desktop', '470'],
        ['2026-09-07', 'mobile', '330'],
        ['2026-09-07', 'tablet', '72'],
      ],
      truncated: false,
    },
  },
]

export default function Area({ data, props }: WidgetProps<AreaProps>) {
  const sql = data as SqlData
  const records = toRecords(sql, contract)
  // An SVG id, so only characters url(#...) takes as they are.
  const fade = 'area' + useId().replace(/[^a-zA-Z0-9_-]/g, '')
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const curve = props.curve ?? 'monotone'
  const hasSeries = sql.columns.includes('series')
  const { rows, keys } = hasSeries ? pivot(records, 'x', 'series', 'y') : { rows: records, keys: ['y'] }
  const config = hasSeries ? seriesConfig(keys) : { y: { label: 'y' } }
  // Overlapping areas each keep a lighter wash so the ones behind still show.
  const ticks = niceTicks(props.stacked ? stackTotals(rows, keys) : valuesOf(rows, keys))
  const wash = props.stacked || keys.length === 1 ? 0.32 : 0.16

  return (
    <ChartContainer config={config} className="h-full w-full">
      <AreaChart data={rows} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
        <defs>
          {/* Each color fades toward the axis, like light into deep water. */}
          {keys.map((_, i) => (
            <linearGradient key={i} id={`${fade}-${i}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={seriesColor(i)} stopOpacity={wash} />
              <stop offset="100%" stopColor={seriesColor(i)} stopOpacity={0.02} />
            </linearGradient>
          ))}
        </defs>
        <CartesianGrid {...grid} />
        <XAxis dataKey="x" {...axis} interval="preserveStartEnd" minTickGap={24} tickFormatter={formatTick} />
        <YAxis {...valueAxis} ticks={ticks} domain={[ticks[0], ticks[ticks.length - 1]]} tickFormatter={(v: number) => formatValue(v, format)} />
        <ChartTooltip
          cursor={{ strokeWidth: 1 }}
          content={tooltip(format, { indicator: 'line', heading: 'x', total: props.stacked })}
        />
        {hasSeries && legend()}
        {keys.map((key, i) => (
          <RechartsArea
            key={key}
            type={curve}
            dataKey={key}
            stackId={props.stacked ? 'stack' : undefined}
            fill={`url(#${fade}-${i})`}
            stroke={seriesColor(i)}
            strokeWidth={2}
            activeDot={activeDot(seriesColor(i))}
            isAnimationActive={false}
          />
        ))}
      </AreaChart>
    </ChartContainer>
  )
}
