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
import type { Contract, SqlData, WidgetProps } from './types'

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
