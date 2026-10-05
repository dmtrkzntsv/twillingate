import { Cell, Label, Pie as RechartsPie, PieChart } from 'recharts'
import {
  ChartContainer,
  ChartTooltip,
  type ChartConfig,
} from '@/components/ui/chart'
import { seriesColor } from '@/lib/chart'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import { legend, tooltip } from '@/components/chart-parts'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface PieProps {
  format?: Format
  donut?: boolean
}

export const contract: Contract = {
  description: "Parts of a whole, up to 5 slices, one per chart color; group the rest as 'Other' in SQL.",
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
  const donut = props.donut ?? false
  // Only `label`, no `color`: a data label is arbitrary text, not safe as
  // the CSS custom property name ChartContainer would derive from it.
  // Slices are colored directly via `fill` on each Cell.
  const config: ChartConfig = Object.fromEntries(records.map((r) => [String(r.label), { label: String(r.label) }]))
  const total = records.reduce((sum, r) => sum + Number(r.value ?? 0), 0)

  return (
    <ChartContainer config={config} className="h-full w-full">
      <PieChart>
        <ChartTooltip content={tooltip(format, { nameKey: 'label' })} />
        {legend('label')}
        <RechartsPie
          data={records}
          dataKey="value"
          nameKey="label"
          innerRadius={donut ? '64%' : 0}
          outerRadius="88%"
          cornerRadius={donut ? 4 : 0}
          // The card's color between slices: a gap, not an outline.
          stroke="var(--card)"
          strokeWidth={2}
          isAnimationActive={false}
        >
          {records.map((r, i) => (
            <Cell key={String(r.label)} fill={seriesColor(i)} />
          ))}
          {donut && (
            <Label
              content={({ viewBox }) => {
                const { cx, cy } = viewBox as { cx: number; cy: number }
                return (
                  <text x={cx} y={cy} textAnchor="middle" dominantBaseline="middle">
                    <tspan x={cx} y={cy - 4} className="fill-foreground text-xl font-semibold">
                      {formatValue(total, format)}
                    </tspan>
                    <tspan x={cx} y={cy + 16} className="fill-muted-foreground text-xs">
                      Total
                    </tspan>
                  </text>
                )
              }}
            />
          )}
        </RechartsPie>
      </PieChart>
    </ChartContainer>
  )
}
