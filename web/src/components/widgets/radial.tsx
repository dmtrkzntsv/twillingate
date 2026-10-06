import { PolarAngleAxis, RadialBar, RadialBarChart } from 'recharts'
import {
  ChartContainer,
  ChartTooltip,
  type ChartConfig,
} from '@/components/ui/chart'
import { seriesColor } from '@/lib/chart'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import { legend, tooltip } from '@/components/chart-parts'
import { useCardMode } from '@/components/share/card-mode'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface RadialProps {
  format?: Format
}

export const contract: Contract = {
  description: 'A single value as a ring toward a `max` target; use for a goal or capacity metric.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'label', types: ['text'] },
      { name: 'value', types: ['number'] },
      { name: 'max', types: ['number'], optional: true },
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
    title: 'Signups toward goal',
    props: {},
    data: {
      columns: ['label', 'value', 'max'],
      rows: [
        ['September', '420', '600'],
        ['August', '580', '600'],
        ['July', '495', '500'],
      ],
      truncated: false,
    },
  },
]

export default function Radial({ data, props }: WidgetProps<RadialProps>) {
  const sql = data as SqlData
  const records = toRecords(sql, contract)
  const card = useCardMode()
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const hasMax = sql.columns.includes('max')

  const rows = records.map((r, i) => {
    const value = Number(r.value ?? 0)
    const max = hasMax ? Number(r.max ?? 0) || 1 : undefined
    return {
      label: String(r.label),
      value,
      display: max ? Math.min(100, (value / max) * 100) : value,
      fill: seriesColor(i),
    }
  })
  const domainMax = hasMax ? 100 : Math.max(...rows.map((r) => r.display), 1)
  const single = rows.length === 1

  // Only `label`, no `color`: colors are set directly per bar via `fill`
  // below, and a data label can hold arbitrary text (spaces, punctuation)
  // that isn't safe as a CSS custom property name, which is how
  // ChartContainer would otherwise wire up a `color`.
  const config: ChartConfig = Object.fromEntries(rows.map((r) => [r.label, { label: r.label }]))

  return (
    <ChartContainer config={config} className="h-full w-full">
      <RadialBarChart
        data={rows}
        innerRadius={single ? '72%' : '30%'}
        outerRadius={single ? '96%' : '92%'}
        startAngle={90}
        endAngle={-270}
        barCategoryGap={single ? 0 : '18%'}
      >
        <PolarAngleAxis type="number" domain={[0, domainMax]} tick={false} />
        <ChartTooltip cursor={false} content={tooltip(format, { nameKey: 'label', valueKey: 'value' })} />
        {!single && legend('label')}
        <RadialBar dataKey="display" background cornerRadius={999} isAnimationActive={false} />
        {single && (
          <text x="50%" y="50%" textAnchor="middle" dominantBaseline="middle">
            <tspan x="50%" dy="-0.3em" className={`fill-foreground font-semibold ${card ? 'text-[40px]' : 'text-2xl'}`}>
              {formatValue(rows[0].value, format)}
            </tspan>
            <tspan x="50%" dy="1.6em" className={`fill-muted-foreground ${card ? 'text-[16px]' : 'text-xs'}`}>
              {hasMax ? `${Math.round(rows[0].display)}% of ${formatValue(Number(records[0].max), format)}` : rows[0].label}
            </tspan>
          </text>
        )}
      </RadialBarChart>
    </ChartContainer>
  )
}
