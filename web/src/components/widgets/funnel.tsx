import { Cell, Funnel as RechartsFunnel, FunnelChart, LabelList } from 'recharts'
import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from '@/components/ui/chart'
import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface FunnelProps {
  format?: Format
}

export const contract: Contract = {
  description: 'A drop-off funnel, steps in query order; a signup or checkout flow.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'step', types: ['text'] },
      { name: 'value', types: ['number'] },
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

export const examples: Example[] = [
  {
    title: 'Signup funnel',
    props: { format: 'number' },
    data: {
      columns: ['step', 'value'],
      rows: [
        ['Visited', '5000'],
        ['Viewed pricing', '1800'],
        ['Started signup', '640'],
        ['Signed up', '410'],
      ],
      truncated: false,
    },
  },
]

export default function Funnel({ data, props }: WidgetProps<FunnelProps>) {
  const records = toRecords(data as SqlData, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const first = Number(records[0].value ?? 0) || 1
  const rows = records.map((r) => {
    const value = Number(r.value ?? 0)
    return {
      step: String(r.step),
      value,
      share: formatValue(value / first, 'percent'),
    }
  })

  // Only `label`, no `color` (see radial.tsx): a step name is arbitrary
  // text, colored directly per Cell below instead.
  const config: ChartConfig = Object.fromEntries(rows.map((r) => [r.step, { label: r.step }]))

  return (
    <ChartContainer config={config} className="h-full w-full">
      <FunnelChart>
        <ChartTooltip
          content={<ChartTooltipContent nameKey="step" formatter={(v) => formatValue(Number(v), format)} />}
        />
        <RechartsFunnel data={rows} dataKey="value" nameKey="step" isAnimationActive={false}>
          <LabelList dataKey="step" position="right" fill="var(--foreground)" stroke="none" />
          <LabelList dataKey="share" position="left" fill="var(--foreground)" stroke="none" />
          {rows.map((r, i) => (
            <Cell key={r.step} fill={`var(--chart-${(i % 5) + 1})`} />
          ))}
        </RechartsFunnel>
      </FunnelChart>
    </ChartContainer>
  )
}
