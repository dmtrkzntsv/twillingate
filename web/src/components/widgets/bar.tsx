import { Bar as RechartsBar, BarChart, CartesianGrid, XAxis, YAxis } from 'recharts'
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
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface BarProps {
  format?: Format
  horizontal?: boolean
  stacked?: boolean
}

export const contract: Contract = {
  description:
    'Compare values across categories; use `series` for grouped or `stacked` bars, `horizontal` for long labels.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'x', types: ['text', 'day'] },
      { name: 'y', types: ['number'] },
      { name: 'series', types: ['text'], optional: true },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
      horizontal: { type: 'boolean' },
      stacked: { type: 'boolean' },
    },
    additionalProperties: false,
  },
  defaultWidth: 6,
  defaultHeight: 8,
}

export const examples: Example[] = [
  {
    title: 'Daily active users, new and returning',
    props: { stacked: true },
    data: {
      columns: ['x', 'series', 'y'],
      rows: [
        ['2026-09-01', 'new', '85'],
        ['2026-09-01', 'returning', '210'],
        ['2026-09-02', 'new', '92'],
        ['2026-09-02', 'returning', '225'],
        ['2026-09-03', 'new', '78'],
        ['2026-09-03', 'returning', '230'],
        ['2026-09-04', 'new', '104'],
        ['2026-09-04', 'returning', '240'],
        ['2026-09-05', 'new', '112'],
        ['2026-09-05', 'returning', '255'],
        ['2026-09-06', 'new', '95'],
        ['2026-09-06', 'returning', '220'],
        ['2026-09-07', 'new', '120'],
        ['2026-09-07', 'returning', '260'],
      ],
      truncated: false,
    },
  },
  {
    title: 'Top product events',
    props: { horizontal: true },
    data: {
      columns: ['x', 'y'],
      rows: [
        ['page_view', '3200'],
        ['search', '610'],
        ['signup', '480'],
        ['checkout_started', '260'],
        ['checkout_completed', '150'],
        ['share', '95'],
      ],
      truncated: false,
    },
  },
]

export default function Bar({ data, props }: WidgetProps<BarProps>) {
  const sql = data as SqlData
  const records = toRecords(sql, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const horizontal = props.horizontal ?? false
  const hasSeries = sql.columns.includes('series')
  const { rows, keys } = hasSeries ? pivot(records, 'x', 'series', 'y') : { rows: records, keys: ['y'] }
  const config = hasSeries ? seriesConfig(keys) : { y: { label: 'y', color: 'var(--chart-1)' } }

  const valueAxis = (
    <YAxis
      type="number"
      tickFormatter={(v: number) => formatValue(v, format)}
      tickLine={false}
      axisLine={false}
      width={56}
    />
  )
  const categoryAxis = (
    <XAxis dataKey="x" interval="preserveStartEnd" minTickGap={32} tickLine={false} axisLine={false} />
  )

  return (
    <ChartContainer config={config} className="h-full w-full">
      <BarChart data={rows} layout={horizontal ? 'vertical' : 'horizontal'}>
        <CartesianGrid vertical={horizontal} horizontal={!horizontal} />
        {horizontal ? (
          <>
            <XAxis
              type="number"
              tickFormatter={(v: number) => formatValue(v, format)}
              tickLine={false}
              axisLine={false}
            />
            <YAxis
              dataKey="x"
              type="category"
              tickLine={false}
              axisLine={false}
              width={96}
              interval="preserveStartEnd"
            />
          </>
        ) : (
          <>
            {categoryAxis}
            {valueAxis}
          </>
        )}
        <ChartTooltip content={<ChartTooltipContent />} />
        {hasSeries && <ChartLegend content={<ChartLegendContent />} />}
        {keys.map((key, i) => (
          <RechartsBar
            key={key}
            dataKey={key}
            stackId={props.stacked ? 'stack' : undefined}
            fill={`var(--chart-${(i % 5) + 1})`}
            isAnimationActive={false}
          />
        ))}
      </BarChart>
    </ChartContainer>
  )
}
