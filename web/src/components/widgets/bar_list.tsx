import { HoverCard, useHover, type HoverRow } from '@/components/chart-parts'
import { formatExact, formatValue, type Format } from '@/lib/format'
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
  const { hovered, bind } = useHover<number>()
  const records = toRecords(data as SqlData, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const values = records.map((r) => Number(r.value ?? 0))
  const max = Math.max(...values, 1)
  // A share only of counts, and only of a whole list: a cut list has no total.
  const sum = values.reduce((a, b) => a + b, 0)
  const shares = format === 'number' && !(data as SqlData).truncated && sum > 0

  const rowsOf = (i: number): HoverRow[] => [
    { label: 'Value', value: formatExact(values[i], format), color: 'var(--chart-1)' },
    ...(shares ? [{ label: 'Share of list', value: formatValue(values[i] / sum, 'percent') }] : []),
    { label: 'Rank', value: `${i + 1} of ${records.length}` },
  ]

  return (
    <>
      <ul className="flex h-full flex-col gap-1 overflow-y-auto py-1">
        {records.map((r, i) => (
          <li
            key={i}
            {...bind(i)}
            className="group relative flex min-h-8 items-center justify-between gap-3 overflow-hidden rounded-sm px-2.5 text-sm hover:bg-muted/60"
          >
            <div
              data-bar-fill
              className="absolute inset-y-0 left-0 rounded-sm bg-[var(--chart-1)]/15 transition-colors group-hover:bg-[var(--chart-1)]/30"
              style={{ width: `${(values[i] / max) * 100}%` }}
            />
            <span className="relative truncate">{String(r.label)}</span>
            <span className="relative shrink-0 font-medium tabular-nums">{formatValue(values[i], format)}</span>
          </li>
        ))}
      </ul>
      {hovered && hovered.item < records.length && (
        <HoverCard at={hovered} heading={String(records[hovered.item].label)} rows={rowsOf(hovered.item)} />
      )}
    </>
  )
}
