import { HoverCard, useHover, type HoverRow } from '@/components/chart-parts'
import { approx, approximate, formatExact, formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import { useCardMode } from '@/components/share/card-mode'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface FunnelProps {
  format?: Format
}

export const contract: Contract = {
  description: 'A drop-off funnel, steps in query order; a signup or checkout flow.',
  accepts: ['sql', 'query'],
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
  const { hovered, bind } = useHover<number>()
  const card = useCardMode()
  const records = toRecords(data as SqlData, contract)
  const estimated = approximate(data as SqlData, 'value')
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const values = records.map((r) => Number(r.value ?? 0))
  const first = values[0] || 1
  const last = Math.max(values.length - 1, 1)

  const rowsOf = (i: number): HoverRow[] => {
    const rows: HoverRow[] = [
      { label: 'Value', value: approx(formatExact(values[i], format), estimated), color: 'var(--chart-1)' },
      { label: 'Of the first step', value: formatValue(values[i] / first, 'percent') },
    ]
    if (i > 0 && values[i - 1]) {
      rows.push({ label: 'Of the step before', value: formatValue(values[i] / values[i - 1], 'percent') })
      // Only counts drop off: a difference of percents or durations is not a loss.
      if (format === 'number') rows.push({ label: 'Dropped', value: approx(formatExact(values[i - 1] - values[i]), estimated) })
    }
    return rows
  }

  const widthOf = (i: number) => Math.max(0, Math.min(1, values[i] / first)) * 100
  // One hue, fading step by step: the order is the story, not the color.
  const colorOf = (i: number) => `color-mix(in oklab, var(--chart-1) ${100 - (i / last) * 40}%, transparent)`

  return (
    <>
      <ol className="flex h-full flex-col justify-center px-1 py-1">
        {records.map((r, i) => {
          const value = values[i]
          const share = value / first
          const fromPrevious = i > 0 && values[i - 1] ? value / values[i - 1] : null
          return (
            <li
              key={i}
              data-funnel-step
              {...bind(i)}
              className={`-mx-1.5 flex flex-col rounded-md px-1.5 hover:bg-muted/60 ${i > 0 ? 'max-h-24 flex-[1.5]' : 'max-h-14 flex-1'}`}
            >
              {i > 0 && (
                // The neck from the step before: it narrows by what dropped off.
                <div className={`grid min-h-3 flex-[0.5] grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-3 ${card ? 'min-h-5' : ''}`}>
                  <span
                    className={`self-center truncate text-muted-foreground tabular-nums ${card ? 'text-[length:var(--card-type)]' : 'text-[11px]'}`}
                  >
                    {fromPrevious !== null && `${formatValue(fromPrevious, 'percent')} converted`}
                  </span>
                  <div
                    data-funnel-neck
                    style={{
                      backgroundColor: `color-mix(in oklab, ${colorOf(i)} 22%, transparent)`,
                      clipPath: `polygon(${50 - widthOf(i - 1) / 2}% 0, ${50 + widthOf(i - 1) / 2}% 0, ${50 + widthOf(i) / 2}% 100%, ${50 - widthOf(i) / 2}% 100%)`,
                    }}
                  />
                </div>
              )}
              <div className={`grid flex-1 grid-cols-[minmax(0,2fr)_minmax(0,3fr)] items-center gap-3 ${card ? 'min-h-14' : 'min-h-9'}`}>
                <div className={`flex min-w-0 flex-col leading-tight ${card ? 'text-[22px]' : 'text-sm'}`}>
                  <span className="truncate font-medium">{String(r.step)}</span>
                  <span className={`truncate tabular-nums text-muted-foreground ${card ? 'text-[length:var(--card-type)]' : 'text-xs'}`}>
                    {approx(formatValue(value, format), estimated)}
                    {' · '}
                    <span className="font-medium text-foreground">{formatValue(share, 'percent')}</span>
                  </span>
                </div>
                <div className="flex h-full items-stretch justify-center">
                  <div
                    data-bar-fill
                    className="min-w-0.5 rounded-[3px]"
                    style={{ width: `${widthOf(i)}%`, backgroundColor: colorOf(i) }}
                  />
                </div>
              </div>
            </li>
          )
        })}
      </ol>
      {hovered && hovered.item < records.length && (
        <HoverCard at={hovered} heading={String(records[hovered.item].step)} rows={rowsOf(hovered.item)} />
      )}
    </>
  )
}
