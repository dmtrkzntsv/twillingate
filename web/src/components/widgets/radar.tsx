import { PolarAngleAxis, PolarGrid, Radar as RechartsRadar, RadarChart } from 'recharts'
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

interface RadarProps {
  format?: Format
}

export const contract: Contract = {
  description:
    'Compare several axes at once, one shape per `series` value; keep the number of axes small (4–8) for readability.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'axis', types: ['text'] },
      { name: 'value', types: ['number'] },
      { name: 'series', types: ['text'], optional: true },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
    },
    additionalProperties: false,
  },
  defaultWidth: 4,
  defaultHeight: 8,
}

export const examples: Example[] = [
  {
    title: 'Visitors by weekday',
    props: {},
    data: {
      columns: ['axis', 'series', 'value'],
      rows: [
        ['Mon', 'this week', '320'],
        ['Mon', 'last week', '300'],
        ['Tue', 'this week', '340'],
        ['Tue', 'last week', '315'],
        ['Wed', 'this week', '360'],
        ['Wed', 'last week', '330'],
        ['Thu', 'this week', '355'],
        ['Thu', 'last week', '340'],
        ['Fri', 'this week', '410'],
        ['Fri', 'last week', '380'],
        ['Sat', 'this week', '250'],
        ['Sat', 'last week', '230'],
        ['Sun', 'this week', '210'],
        ['Sun', 'last week', '195'],
      ],
      truncated: false,
    },
  },
]

export default function Radar({ data, props }: WidgetProps<RadarProps>) {
  const sql = data as SqlData
  const records = toRecords(sql, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const hasSeries = sql.columns.includes('series')
  const { rows, keys } = hasSeries
    ? pivot(records, 'axis', 'series', 'value')
    : { rows: records, keys: ['value'] }
  const config = hasSeries ? seriesConfig(keys) : { value: { label: 'value', color: 'var(--chart-1)' } }

  return (
    <ChartContainer config={config} className="h-full w-full">
      <RadarChart data={rows}>
        <PolarGrid />
        <PolarAngleAxis dataKey="axis" />
        <ChartTooltip content={<ChartTooltipContent formatter={(v) => formatValue(Number(v), format)} />} />
        {hasSeries && <ChartLegend content={<ChartLegendContent />} />}
        {keys.map((key, i) => (
          <RechartsRadar
            key={key}
            dataKey={key}
            stroke={`var(--chart-${(i % 5) + 1})`}
            fill={`var(--chart-${(i % 5) + 1})`}
            fillOpacity={0.3}
            isAnimationActive={false}
          />
        ))}
      </RadarChart>
    </ChartContainer>
  )
}
