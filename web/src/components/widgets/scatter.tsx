import { Scatter as RechartsScatter, ScatterChart, XAxis, YAxis, ZAxis } from 'recharts'
import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
} from '@/components/ui/chart'
import { seriesConfig } from '@/lib/chart'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
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

  return (
    <ChartContainer config={config} className="h-full w-full">
      <ScatterChart>
        <XAxis
          dataKey="x"
          type="number"
          name="x"
          tickFormatter={(v: number) => formatValue(v, format)}
          tickLine={false}
          axisLine={false}
          interval="preserveStartEnd"
          minTickGap={32}
        />
        <YAxis
          dataKey="y"
          type="number"
          name="y"
          tickFormatter={(v: number) => formatValue(v, format)}
          tickLine={false}
          axisLine={false}
          width={56}
        />
        {hasSize && <ZAxis dataKey="size" type="number" range={[64, 400]} />}
        <ChartTooltip content={<ChartTooltipContent />} cursor={{ strokeDasharray: '3 3' }} />
        {hasSeries && <ChartLegend content={<ChartLegendContent />} />}
        {groups.map(({ key, data: groupData }, i) => (
          <RechartsScatter
            key={key}
            name={key}
            data={groupData}
            fill={`var(--chart-${(i % 5) + 1})`}
            isAnimationActive={false}
          />
        ))}
      </ScatterChart>
    </ChartContainer>
  )
}
