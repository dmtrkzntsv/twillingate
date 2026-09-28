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

export const examples: Example[] = [
  {
    title: 'Pages: views vs time on page',
    props: { format: 'duration' },
    data: {
      columns: ['x', 'y', 'series', 'size'],
      rows: [
        ['450', '95', 'docs', '40'],
        ['620', '110', 'docs', '55'],
        ['310', '80', 'docs', '30'],
        ['780', '130', 'docs', '70'],
        ['200', '60', 'docs', '20'],
        ['540', '105', 'docs', '48'],
        ['1200', '45', 'marketing', '90'],
        ['900', '55', 'marketing', '75'],
        ['1500', '40', 'marketing', '110'],
        ['700', '60', 'marketing', '60'],
        ['1100', '50', 'marketing', '85'],
        ['950', '48', 'marketing', '78'],
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
