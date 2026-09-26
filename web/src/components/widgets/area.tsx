import { Area as RechartsArea, AreaChart, CartesianGrid, XAxis, YAxis } from 'recharts'
import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from '@/components/ui/chart'
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

// Only `label`, no `color` (see radial.tsx / pie.tsx): a series value is
// arbitrary text, not safe as the CSS custom property name ChartContainer
// would derive from it. Each line/area/bar is colored directly, cycling
// `var(--chart-1..5)`.
function seriesConfig(keys: string[]): ChartConfig {
  return Object.fromEntries(keys.map((key) => [key, { label: key }]))
}

export default function Area({ data, props }: WidgetProps<AreaProps>) {
  const sql = data as SqlData
  const records = toRecords(sql, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const curve = props.curve ?? 'linear'
  const hasSeries = sql.columns.includes('series')
  const { rows, keys } = hasSeries ? pivot(records, 'x', 'series', 'y') : { rows: records, keys: ['y'] }
  const config = hasSeries ? seriesConfig(keys) : { y: { label: 'y', color: 'var(--chart-1)' } }

  return (
    <ChartContainer config={config} className="h-full w-full">
      <AreaChart data={rows}>
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
            fill={`var(--chart-${(i % 5) + 1})`}
            stroke={`var(--chart-${(i % 5) + 1})`}
            fillOpacity={0.4}
            isAnimationActive={false}
          />
        ))}
      </AreaChart>
    </ChartContainer>
  )
}
