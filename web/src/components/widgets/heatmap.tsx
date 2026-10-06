import { HoverCard, useHover } from '@/components/chart-parts'
import { formatExact, formatValue, type Format } from '@/lib/format'
import { toRecords } from '@/lib/records'
import { formatTick, onRamp, ramp } from '@/lib/chart'
import { useCardMode } from '@/components/share/card-mode'
import type { Contract, Example, SqlData, WidgetProps } from './types'

interface HeatmapProps {
  format?: Format
  labels?: boolean
}

export const contract: Contract = {
  description: 'A shaded grid of x by y, e.g. a retention cohort: cohort day x days since.',
  accepts: ['sql'],
  inputs: {
    open: false,
    columns: [
      { name: 'x', types: ['text', 'day'] },
      { name: 'y', types: ['text', 'day'] },
      { name: 'value', types: ['number'] },
    ],
  },
  props: {
    type: 'object',
    properties: {
      format: { enum: ['number', 'percent', 'duration'] },
      labels: { type: 'boolean' },
    },
    additionalProperties: false,
  },
  defaultWidth: 6,
  defaultHeight: 10,
}

// Retention by cohort: only cells where cohort day + days since falls within
// the tracked window (2026-09-01..06), so the grid is the usual triangle.
export const examples: Example[] = [
  {
    title: 'Retention by cohort',
    props: { format: 'percent', labels: true },
    data: {
      columns: ['x', 'y', 'value'],
      rows: [
        ['2026-09-01', '0', '1.00'],
        ['2026-09-01', '1', '0.46'],
        ['2026-09-01', '2', '0.33'],
        ['2026-09-01', '3', '0.27'],
        ['2026-09-01', '4', '0.22'],
        ['2026-09-01', '5', '0.18'],
        ['2026-09-02', '0', '1.00'],
        ['2026-09-02', '1', '0.44'],
        ['2026-09-02', '2', '0.31'],
        ['2026-09-02', '3', '0.25'],
        ['2026-09-02', '4', '0.20'],
        ['2026-09-03', '0', '1.00'],
        ['2026-09-03', '1', '0.43'],
        ['2026-09-03', '2', '0.30'],
        ['2026-09-03', '3', '0.24'],
        ['2026-09-04', '0', '1.00'],
        ['2026-09-04', '1', '0.42'],
        ['2026-09-04', '2', '0.29'],
        ['2026-09-05', '0', '1.00'],
        ['2026-09-05', '1', '0.41'],
        ['2026-09-06', '0', '1.00'],
      ],
      truncated: false,
    },
  },
]

function firstSeen(values: string[]): string[] {
  const seen: string[] = []
  const known = new Set<string>()
  for (const v of values) {
    if (!known.has(v)) {
      known.add(v)
      seen.push(v)
    }
  }
  return seen
}

export default function Heatmap({ data, props }: WidgetProps<HeatmapProps>) {
  const { hovered, bind } = useHover<{ x: string; y: string; value: number; t: number }>()
  const card = useCardMode()
  const records = toRecords(data as SqlData, contract)
  if (records.length === 0) return null

  const format = props.format ?? 'number'
  const showLabels = props.labels ?? false

  const xs = firstSeen(records.map((r) => String(r.x)))
  const ys = firstSeen(records.map((r) => String(r.y)))
  const values = new Map<string, number | null>()
  for (const r of records) {
    values.set(`${String(r.y)}\u0000${String(r.x)}`, r.value === null ? null : Number(r.value))
  }

  // A NULL cell is drawn empty, so it takes no part in the scale either.
  const numbers = [...values.values()].filter((v): v is number => v !== null)
  const min = numbers.length ? Math.min(...numbers) : 0
  const max = numbers.length ? Math.max(...numbers) : 0
  const span = max - min || 1

  return (
    <div className={`h-full w-full p-1 ${card ? 'overflow-hidden' : 'overflow-auto'}`}>
      {hovered && (
        <HoverCard
          at={hovered}
          heading={`${formatTick(hovered.item.y)}, ${formatTick(hovered.item.x)}`}
          rows={[{ label: 'Value', value: formatExact(hovered.item.value, format), color: ramp(hovered.item.t) }]}
        />
      )}
      <div
        className="grid h-full gap-0.5"
        style={{
          gridTemplateColumns: `auto repeat(${xs.length}, minmax(2rem, 1fr))`,
          gridTemplateRows: `auto repeat(${ys.length}, minmax(1.5rem, 1fr))`,
        }}
      >
        <div />
        {xs.map((x) => (
          <div
            key={x}
            data-col-header
            className={`truncate px-1 text-center text-muted-foreground ${card ? 'text-[15px]' : 'text-xs'}`}
          >
            {formatTick(x)}
          </div>
        ))}
        {ys.map((y) => (
          <div key={y} className="contents">
            <div data-row-header className={`flex items-center truncate pr-1.5 text-muted-foreground ${card ? 'text-[15px]' : 'text-xs'}`}>
              {formatTick(y)}
            </div>
            {xs.map((x) => {
              const value = values.get(`${y}\u0000${x}`)
              const hasValue = value !== undefined && value !== null
              const t = hasValue ? (value - min) / span : 0
              return (
                <div
                  key={x}
                  data-cell
                  {...(hasValue ? bind({ x, y, value, t }) : {})}
                  className={`flex min-h-6 items-center justify-center rounded-[3px] tabular-nums ${card ? 'text-[15px]' : 'text-[11px]'} ${
                    hasValue && onRamp(t) ? 'text-white' : 'text-foreground'
                  } ${hasValue ? 'hover:ring-2 hover:ring-foreground/50 hover:ring-inset' : ''}`}
                  style={hasValue ? { backgroundColor: ramp(t) } : undefined}
                >
                  {hasValue && showLabels ? formatValue(value, format) : ''}
                </div>
              )
            })}
          </div>
        ))}
      </div>
    </div>
  )
}
