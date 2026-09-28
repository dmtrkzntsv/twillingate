import { Bar, CartesianGrid, ComposedChart, Line, XAxis, YAxis } from 'recharts'
import { ChartContainer, ChartTooltip, ChartTooltipContent } from '@/components/ui/chart'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import type { Contract, Example, SqlData, WidgetProps } from './types'

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

export const examples: Example[] = [
  {
    title: 'Visitors and bounce rate',
    props: { bar_format: 'number', line_format: 'percent' },
    data: {
      columns: ['x', 'bar', 'line'],
      rows: [
        ['2026-09-01', '240', '0.52'],
        ['2026-09-02', '255', '0.50'],
        ['2026-09-03', '260', '0.49'],
        ['2026-09-04', '275', '0.47'],
        ['2026-09-05', '300', '0.46'],
        ['2026-09-06', '290', '0.48'],
        ['2026-09-07', '310', '0.45'],
        ['2026-09-08', '320', '0.44'],
        ['2026-09-09', '335', '0.43'],
        ['2026-09-10', '340', '0.42'],
        ['2026-09-11', '355', '0.41'],
        ['2026-09-12', '360', '0.40'],
        ['2026-09-13', '375', '0.39'],
        ['2026-09-14', '390', '0.38'],
      ],
      truncated: false,
    },
  },
]

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
