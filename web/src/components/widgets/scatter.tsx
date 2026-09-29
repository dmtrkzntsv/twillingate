import { CartesianGrid, Scatter as RechartsScatter, ScatterChart, XAxis, YAxis, ZAxis } from 'recharts'
import {
  ChartContainer,
  ChartTooltip,
} from '@/components/ui/chart'
import { axis, niceTicks, seriesColor, seriesConfig, valueAxis } from '@/lib/chart'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import { legend, tooltip } from '@/components/chart-parts'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface ScatterProps {
  format?: Format
}

export const contract: Contract = {
  description:
    'Correlation between two numeric measures; `size` adds a third dimension, `series` colors groups.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'x', types: ['number'] },
      { name: 'y', types: ['number'] },
      { name: 'series', types: ['text'], optional: true },
      { name: 'size', types: ['number'], optional: true },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
    },
    additionalProperties: false,
  },
  defaultWidth: 6,
  defaultHeight: 8,
}

// x (visitors, a count) and y (views per visitor, a rate) share one
// `format` since a scatter's two axes use the same tickFormatter; both
// are kept in plain numbers here rather than mixing a count with a duration.
export const examples: Example[] = [
  {
    title: 'Pages: visitors vs views per visitor',
    props: { format: 'number' },
    data: {
      columns: ['x', 'y', 'series', 'size'],
      rows: [
        ['450', '2.3', 'docs', '40'],
        ['620', '2.8', 'docs', '55'],
        ['310', '1.9', 'docs', '30'],
        ['780', '3.1', 'docs', '70'],
        ['200', '1.5', 'docs', '20'],
        ['540', '2.6', 'docs', '48'],
        ['1200', '1.2', 'marketing', '90'],
        ['900', '1.4', 'marketing', '75'],
        ['1500', '1.1', 'marketing', '110'],
        ['700', '1.6', 'marketing', '60'],
        ['1100', '1.3', 'marketing', '85'],
        ['950', '1.35', 'marketing', '78'],
      ],
      truncated: false,
    },
  },
]

export default function Scatter({ data, props }: WidgetProps<ScatterProps>) {
  const sql = data as SqlData
  const records = toRecords(sql, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const hasSeries = sql.columns.includes('series')
  const hasSize = sql.columns.includes('size')

  const keys = hasSeries ? [...new Set(records.map((r) => String(r.series)))] : ['value']
  const groups = keys.map((key) => ({
    key,
    data: hasSeries ? records.filter((r) => String(r.series) === key) : records,
  }))
  const config = seriesConfig(keys)
  const xTicks = niceTicks(records.map((r) => Number(r.x)))
  const yTicks = niceTicks(records.map((r) => Number(r.y)))
  const show = (v: number) => formatValue(v, format)

  return (
    <ChartContainer config={config} className="h-full w-full">
      <ScatterChart margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
        <CartesianGrid />
        <XAxis
          dataKey="x"
          type="number"
          name="x"
          {...axis}
          ticks={xTicks}
          domain={[xTicks[0], xTicks[xTicks.length - 1]]}
          tickFormatter={show}
        />
        <YAxis
          dataKey="y"
          type="number"
          name="y"
          {...valueAxis}
          ticks={yTicks}
          domain={[yTicks[0], yTicks[yTicks.length - 1]]}
          tickFormatter={show}
        />
        {hasSize && <ZAxis dataKey="size" type="number" range={[48, 360]} />}
        <ChartTooltip cursor={{ strokeWidth: 1 }} content={tooltip(format)} />
        {hasSeries && legend()}
        {groups.map(({ key, data: groupData }, i) => (
          <RechartsScatter
            key={key}
            name={key}
            data={groupData}
            fill={seriesColor(i)}
            fillOpacity={0.75}
            // A ring in the card's color keeps overlapping points apart.
            stroke="var(--card)"
            strokeWidth={1.5}
            isAnimationActive={false}
          />
        ))}
      </ScatterChart>
    </ChartContainer>
  )
}
