import { useId } from 'react'
import { Area as RechartsArea, AreaChart, CartesianGrid, XAxis, YAxis } from 'recharts'
import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { seriesConfig } from '@/lib/chart'
import { formatValue, type Format } from '@/lib/format'
import { pivot, toRecords } from '@/lib/records'
import type { Contract, SqlData, WidgetProps } from './types'

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

export default function Area({ data, props }: WidgetProps<AreaProps>) {
  const sql = data as SqlData
  const records = toRecords(sql, contract)
  // An SVG id, so only characters url(#...) takes as they are.
  const fade = 'area' + useId().replace(/[^a-zA-Z0-9_-]/g, '')
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const curve = props.curve ?? 'linear'
  const hasSeries = sql.columns.includes('series')
  const { rows, keys } = hasSeries ? pivot(records, 'x', 'series', 'y') : { rows: records, keys: ['y'] }
  const config = hasSeries ? seriesConfig(keys) : { y: { label: 'y', color: 'var(--chart-1)' } }
  const colors = Math.min(keys.length, 5)

  return (
    <ChartContainer config={config} className="h-full w-full">
      <AreaChart data={rows}>
        <defs>
          {/* Each colour fades toward the axis, like light into deep water. */}
          {Array.from({ length: colors }, (_, c) => (
            <linearGradient key={c} id={`${fade}-${c}`} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor={`var(--chart-${c + 1})`} stopOpacity={0.5} />
              <stop offset="100%" stopColor={`var(--chart-${c + 1})`} stopOpacity={0.06} />
            </linearGradient>
          ))}
        </defs>
        <CartesianGrid vertical={false} />
        <XAxis dataKey="x" interval="preserveStartEnd" minTickGap={32} tickLine={false} axisLine={false} />
        <YAxis
          tickFormatter={(v: number) => formatValue(v, format)}
          tickLine={false}
          axisLine={false}
          width={56}
        />
        <ChartTooltip content={<ChartTooltipContent />} />
        {hasSeries && <ChartLegend content={<ChartLegendContent />} />}
        {keys.map((key, i) => (
          <RechartsArea
            key={key}
            type={curve}
            dataKey={key}
            stackId={props.stacked ? 'stack' : undefined}
            fill={`url(#${fade}-${i % 5})`}
            stroke={`var(--chart-${(i % 5) + 1})`}
            strokeWidth={2}
            isAnimationActive={false}
          />
        ))}
      </AreaChart>
    </ChartContainer>
  )
}
