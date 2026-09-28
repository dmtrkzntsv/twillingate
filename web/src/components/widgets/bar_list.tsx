import { formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface BarListProps {
  format?: Format
}

export const contract: Contract = {
  description: 'A ranked list with an inline bar per row: top pages, referrers, top N by value.',
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
    },
    additionalProperties: false,
  },
  defaultWidth: 6,
  defaultHeight: 8,
}

export const examples: Example[] = [
  {
    title: 'Top pages',
    props: { format: 'number' },
    data: {
      columns: ['label', 'value'],
      rows: [
        ['/', '1240'],
        ['/pricing', '860'],
        ['/docs/install', '540'],
        ['/docs', '410'],
        ['/blog', '320'],
        ['/about', '210'],
        ['/docs/api', '150'],
        ['/contact', '90'],
      ],
      truncated: false,
    },
  },
]

export default function BarList({ data, props }: WidgetProps<BarListProps>) {
  const records = toRecords(data as SqlData, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const values = records.map((r) => Number(r.value ?? 0))
  const max = Math.max(...values, 1)

  return (
    <ul className="flex h-full flex-col justify-center gap-1.5 overflow-y-auto p-2">
      {records.map((r, i) => (
        <li
          key={i}
          className="relative flex items-center justify-between gap-2 overflow-hidden rounded-sm bg-muted px-2 py-1 text-sm"
        >
          <div
            data-bar-fill
            className="absolute inset-y-0 left-0 bg-[var(--chart-1)]/25"
            style={{ width: `${(values[i] / max) * 100}%` }}
          />
          <span className="relative truncate">{String(r.label)}</span>
          <span className="relative shrink-0 tabular-nums">{formatValue(values[i], format)}</span>
        </li>
      ))}
    </ul>
  )
}
