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
  const values = records.map((r) => Number(r.value ?? 0))
  const first = values[0] || 1
  const last = Math.max(values.length - 1, 1)

  return (
    <ol className="flex h-full flex-col justify-center gap-3 px-1 py-1">
      {records.map((r, i) => {
        const value = values[i]
        const share = value / first
        const fromPrevious = i > 0 && values[i - 1] ? value / values[i - 1] : null
        return (
          <li key={i} data-funnel-step className="flex flex-col gap-1.5">
            <div className="flex items-baseline justify-between gap-3 text-sm">
              <span className="truncate font-medium">{String(r.step)}</span>
              <span className="flex shrink-0 items-baseline gap-2 tabular-nums">
                <span className="text-muted-foreground">{formatValue(value, format)}</span>
                <span className="w-12 text-right font-medium">{formatValue(share, 'percent')}</span>
              </span>
            </div>
            <div className="h-2 overflow-hidden rounded-full bg-muted">
              <div
                data-bar-fill
                className="h-full rounded-full"
                style={{
                  width: `${Math.max(0, Math.min(1, share)) * 100}%`,
                  // One hue, fading step by step: the order is the story, not the color.
                  backgroundColor: `color-mix(in oklab, var(--chart-1) ${100 - (i / last) * 40}%, transparent)`,
                }}
              />
            </div>
            {fromPrevious !== null && (
              <span className="text-[11px] text-muted-foreground">
                {`${formatValue(fromPrevious, 'percent')} of the step before`}
              </span>
            )}
          </li>
        )
      })}
    </ol>
  )
}
