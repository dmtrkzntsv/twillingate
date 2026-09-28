import { Cell, Pie as RechartsPie, PieChart } from 'recharts'
import {
  ChartContainer,
  ChartLegend,
  ChartLegendContent,
  ChartTooltip,
  ChartTooltipContent,
  type ChartConfig,
} from '@/components/ui/chart'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface PieProps {
  format?: Format
  donut?: boolean
}

export const contract: Contract = {
  description: "Parts of a whole, up to ~7 slices; group the rest as 'Other' in SQL.",
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'label', types: ['text'] },
      { name: 'value', types: ['number'] },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
      donut: { type: 'boolean' },
    },
    additionalProperties: false,
  },
  defaultWidth: 4,
  defaultHeight: 8,
}

export const examples: Example[] = [
  {
    title: 'Visitors by device',
    props: { donut: true },
    data: {
      columns: ['label', 'value'],
      rows: [
        ['desktop', '620'],
        ['mobile', '380'],
        ['tablet', '95'],
        ['Other', '25'],
      ],
      truncated: false,
    },
  },
]

export default function Pie({ data, props }: WidgetProps<PieProps>) {
  const records = toRecords(data as SqlData, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  // Only `label`, no `color` (see radial.tsx): a data label is arbitrary
  // text, not safe as the CSS custom property name ChartContainer would
  // derive from it. Slices are colored directly via `fill` on each Cell.
  const config: ChartConfig = Object.fromEntries(
    records.map((r) => [String(r.label), { label: String(r.label) }])
  )

  return (
    <ChartContainer config={config} className="h-full w-full">
      <PieChart>
        <ChartTooltip content={<ChartTooltipContent formatter={(v) => formatValue(Number(v), format)} />} />
        <ChartLegend content={<ChartLegendContent nameKey="label" />} />
        <RechartsPie
          data={records}
          dataKey="value"
          nameKey="label"
          innerRadius={props.donut ? '55%' : 0}
          outerRadius="80%"
          isAnimationActive={false}
        >
          {records.map((r, i) => (
            <Cell key={String(r.label)} fill={`var(--chart-${(i % 5) + 1})`} />
          ))}
        </RechartsPie>
      </PieChart>
    </ChartContainer>
  )
}
