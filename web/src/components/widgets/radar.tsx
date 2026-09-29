import { PolarAngleAxis, PolarGrid, PolarRadiusAxis, Radar as RechartsRadar, RadarChart } from 'recharts'
import {
  ChartContainer,
  ChartTooltip,
} from '@/components/ui/chart'
import { seriesColor, seriesConfig } from '@/lib/chart'
import type { Format } from '@/lib/format'
import { pivot, toRecords } from '@/lib/records'
import { legend, tooltip } from '@/components/chart-parts'
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
  const config = hasSeries ? seriesConfig(keys) : { value: { label: 'value' } }
  // One shape carries a wash; several stay mostly outline so none hides another.
  const wash = keys.length === 1 ? 0.25 : 0.08

  return (
    <ChartContainer config={config} className="h-full w-full">
      <RadarChart data={rows} outerRadius="72%">
        <PolarGrid gridType="circle" radialLines={false} />
        <PolarAngleAxis dataKey="axis" tick={{ fontSize: 11, fill: 'var(--muted-foreground)' }} />
        {/* The largest value reaches the outer ring. */}
        <PolarRadiusAxis domain={[0, 'dataMax']} tick={false} axisLine={false} />
        <ChartTooltip cursor={false} content={tooltip(format, { heading: 'axis' })} />
        {hasSeries && legend()}
        {keys.map((key, i) => (
          <RechartsRadar
            key={key}
            dataKey={key}
            stroke={seriesColor(i)}
            strokeWidth={2}
            strokeLinejoin="round"
            fill={seriesColor(i)}
            fillOpacity={wash}
            dot={{ r: 3, fill: seriesColor(i), stroke: 'var(--card)', strokeWidth: 2, fillOpacity: 1 }}
            isAnimationActive={false}
          />
        ))}
      </RadarChart>
    </ChartContainer>
  )
}
