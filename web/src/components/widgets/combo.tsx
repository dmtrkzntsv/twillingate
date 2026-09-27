import { Bar, CartesianGrid, ComposedChart, Line, XAxis, YAxis } from 'recharts'
import { ChartContainer, ChartTooltip, ChartTooltipContent } from '@/components/ui/chart'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import type { Contract, SqlData, WidgetProps } from './types'

interface ComboProps {
  bar_format?: Format
  line_format?: Format
}

export const contract: Contract = {
  description:
    'Two related metrics on two axes over the same `x`, e.g. visitors (bar) and bounce rate (line).',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'x', types: ['day', 'text'] },
      { name: 'bar', types: ['number'] },
      { name: 'line', types: ['number'] },
    ],
  },
  props: {
    type: 'object',
    properties: {
      bar_format: { enum: ['number', 'percent', 'duration'] },
      line_format: { enum: ['number', 'percent', 'duration'] },
    },
    additionalProperties: false,
  },
  defaultWidth: 6,
  defaultHeight: 8,
}

export default function Combo({ data, props }: WidgetProps<ComboProps>) {
  const records = toRecords(data as SqlData, contract)
  if (records.length === 0) return null

  const barFormat = props.bar_format ?? 'number'
  const lineFormat = props.line_format ?? 'number'
  const config = {
    bar: { label: 'bar', color: 'var(--chart-1)' },
    line: { label: 'line', color: 'var(--chart-2)' },
  }

  return (
    <ChartContainer config={config} className="h-full w-full">
      <ComposedChart data={records}>
        <CartesianGrid vertical={false} />
        <XAxis dataKey="x" interval="preserveStartEnd" minTickGap={32} tickLine={false} axisLine={false} />
        <YAxis
          yAxisId="bar"
          tickFormatter={(v: number) => formatValue(v, barFormat)}
          tickLine={false}
          axisLine={false}
          width={56}
        />
        <YAxis
          yAxisId="line"
          orientation="right"
          tickFormatter={(v: number) => formatValue(v, lineFormat)}
          tickLine={false}
          axisLine={false}
          width={56}
        />
        <ChartTooltip content={<ChartTooltipContent />} />
        <Bar yAxisId="bar" dataKey="bar" fill="var(--chart-1)" isAnimationActive={false} />
        <Line
          yAxisId="line"
          type="monotone"
          dataKey="line"
          stroke="var(--chart-2)"
          strokeWidth={2}
          dot={false}
          isAnimationActive={false}
        />
      </ComposedChart>
    </ChartContainer>
  )
}
