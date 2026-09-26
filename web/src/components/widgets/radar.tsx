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
import type { Contract, SqlData, WidgetProps } from './types'

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
