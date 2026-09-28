import { CartesianGrid, Line as RechartsLine, LineChart, XAxis, YAxis } from 'recharts'
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

interface LineProps {
  format?: Format
  curve?: 'linear' | 'monotone' | 'step'
}

export const contract: Contract = {
  description:
    'A trend over time or category, one line per `series` value; best for a continuous metric like visitors per day.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'x', types: ['day', 'text'] },
      { name: 'y', types: ['number'] },
      { name: 'series', types: ['text'], optional: true },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
      curve: { enum: ['linear', 'monotone', 'step'] },
    },
    additionalProperties: false,
  },
  defaultWidth: 6,
  defaultHeight: 8,
}

export const examples: Example[] = [
  {
    title: 'Visitors per day',
    props: { format: 'number', curve: 'monotone' },
    data: {
      columns: ['x', 'y'],
      rows: [
        ['2026-09-01', '240'],
        ['2026-09-02', '255'],
        ['2026-09-03', '260'],
        ['2026-09-04', '275'],
        ['2026-09-05', '300'],
        ['2026-09-06', '290'],
        ['2026-09-07', '310'],
        ['2026-09-08', '320'],
        ['2026-09-09', '335'],
        ['2026-09-10', '340'],
        ['2026-09-11', '355'],
        ['2026-09-12', '360'],
        ['2026-09-13', '375'],
        ['2026-09-14', '390'],
      ],
      truncated: false,
    },
  },
  {
    title: 'Visitors per day by kind',
    props: { curve: 'monotone' },
    data: {
      columns: ['x', 'series', 'y'],
      rows: [
        ['2026-09-01', 'web', '190'],
        ['2026-09-01', 'app', '50'],
        ['2026-09-02', 'web', '200'],
        ['2026-09-02', 'app', '55'],
        ['2026-09-03', 'web', '205'],
        ['2026-09-03', 'app', '55'],
        ['2026-09-04', 'web', '215'],
        ['2026-09-04', 'app', '60'],
        ['2026-09-05', 'web', '230'],
        ['2026-09-05', 'app', '70'],
        ['2026-09-06', 'web', '225'],
        ['2026-09-06', 'app', '65'],
        ['2026-09-07', 'web', '240'],
        ['2026-09-07', 'app', '70'],
      ],
      truncated: false,
    },
  },
]

export default function Line({ data, props }: WidgetProps<LineProps>) {
  const sql = data as SqlData
  const records = toRecords(sql, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const curve = props.curve ?? 'linear'
  const hasSeries = sql.columns.includes('series')
  const { rows, keys } = hasSeries ? pivot(records, 'x', 'series', 'y') : { rows: records, keys: ['y'] }
  const config = hasSeries ? seriesConfig(keys) : { y: { label: 'y', color: 'var(--chart-1)' } }

  return (
    <ChartContainer config={config} className="h-full w-full">
      <LineChart data={rows}>
        <CartesianGrid vertical={false} />
        <XAxis dataKey="x" interval="preserveStartEnd" minTickGap={32} tickLine={false} axisLine={false} />
        <YAxis
          tickFormatter={(v: number) => formatValue(v, format)}
          tickLine={false}
          axisLine={false}
          width={56}
        />
        <ChartTooltip content={<ChartTooltipContent />} />
        {hasSeries && <ChartLegend content={<ChartLegendContent />} />}
        {keys.map((key, i) => (
          <RechartsLine
            key={key}
            type={curve}
            dataKey={key}
            stroke={`var(--chart-${(i % 5) + 1})`}
            strokeWidth={2}
            dot={false}
            isAnimationActive={false}
          />
        ))}
      </LineChart>
    </ChartContainer>
  )
}
