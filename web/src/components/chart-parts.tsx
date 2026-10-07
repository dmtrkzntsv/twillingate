import { useEffect, useState, type PointerEvent as ReactPointerEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useCardMode } from '@/components/share/card-mode'
import { ChartLegend, ChartLegendContent, ChartTooltipContent } from '@/components/ui/chart'
import { formatHeading, ramp } from '@/lib/chart'
import { formatValue, type Format } from '@/lib/format'

interface Options {
  /** A line beside each value (trends), or a dot (everything else). */
  indicator?: 'line' | 'dot'
  /** The row key the heading names: a day reads `Sep 1, 2026`. Omit for no heading. */
  heading?: string
  /** A Total row, for stacked charts. */
  total?: boolean
  nameKey?: string
  /** Show this row field rather than the plotted value (e.g. the value behind a share). */
  valueKey?: string
}

/** The one tooltip every chart shows: a heading, then each value in the widget's `format`. */
export function tooltip(format: Format, { indicator = 'dot', heading, total = false, nameKey, valueKey }: Options = {}) {
  return (
    <ChartTooltipContent
      indicator={indicator}
      hideLabel={heading === undefined}
      labelFormatter={(_, payload) => formatHeading(payload?.[0]?.payload?.[heading ?? ''])}
      valueFormatter={(v, row) => formatValue(valueKey && row ? Number(row[valueKey]) : v, format)}
      total={total}
      nameKey={nameKey}
    />
  )
}

/** The legend under a chart of two or more series, in series order. */
export function legend(nameKey?: string) {
  return <ChartLegend itemSorter={null} content={<ChartLegendContent nameKey={nameKey} />} />
}

/** The key to a shaded chart: its smallest and largest value either side of the ramp's steps. */
export function ScaleLegend({ min, max, base }: { min: string; max: string; base?: string }) {
  const card = useCardMode()
  return (
    <div
      data-scale-legend
      className={`flex items-center justify-end text-muted-foreground tabular-nums ${card ? 'gap-2.5 text-[length:var(--card-type)]' : 'gap-1.5 text-[10px]'}`}
    >
      <span>{min}</span>
      <span className={`flex ${card ? 'gap-1' : 'gap-0.5'}`}>
        {[0, 0.25, 0.5, 0.75, 1].map((t) => (
          <span key={t} className={`rounded-[3px] ${card ? 'size-4' : 'size-2.5'}`} style={{ backgroundColor: ramp(t, base) }} />
        ))}
      </span>
      <span>{max}</span>
    </div>
  )
}

/** What a hover card lists under its heading: a name and its value, with an optional color dot. */
export interface HoverRow {
  label: string
  value: string
  color?: string
}

interface Hovered<T> {
  item: T
  x: number
  y: number
}

/**
 * Hover state for a widget drawn without recharts: `bind(item)` goes on each
 * mark, and `hovered` is the mark under the pointer (and where the pointer
 * entered it) until the pointer leaves. The widget re-renders only on
 * crossing into another mark; `HoverCard` follows the pointer on its own.
 */
export function useHover<T>() {
  const [hovered, setHovered] = useState<Hovered<T> | null>(null)
  const bind = (item: T) => ({
    onPointerEnter: (e: ReactPointerEvent) => setHovered({ item, x: e.clientX, y: e.clientY }),
    onPointerLeave: () => setHovered(null),
  })
  return { hovered, bind }
}

/** The gap between the pointer and the card. */
const OFFSET = 12

/**
 * The card every recharts tooltip shows, for the other widgets: a heading
 * and a row per value, beside the pointer and following it, flipped to the
 * pointer's other side past the middle of the window so it stays on screen.
 */
export function HoverCard({ at, heading, rows }: { at: { x: number; y: number }; heading?: ReactNode; rows: HoverRow[] }) {
  const [pointer, setPointer] = useState(at)
  useEffect(() => {
    const move = (e: PointerEvent) => setPointer({ x: e.clientX, y: e.clientY })
    window.addEventListener('pointermove', move)
    return () => window.removeEventListener('pointermove', move)
  }, [])

  const dx = pointer.x > window.innerWidth / 2 ? `calc(-100% - ${OFFSET}px)` : `${OFFSET}px`
  const dy = pointer.y > window.innerHeight / 2 ? `calc(-100% - ${OFFSET}px)` : `${OFFSET}px`

  return createPortal(
    <div
      role="tooltip"
      data-hover-card
      className="pointer-events-none fixed top-0 left-0 z-50 grid max-w-72 min-w-[9rem] items-start gap-1.5 rounded-lg border border-border/70 bg-popover/95 px-2.5 py-2 text-xs text-popover-foreground shadow-lg backdrop-blur-sm"
      style={{ transform: `translate(${pointer.x}px, ${pointer.y}px) translate(${dx}, ${dy})` }}
    >
      {heading !== undefined && <div className="font-medium [overflow-wrap:anywhere]">{heading}</div>}
      <div className="grid gap-1.5">
        {rows.map((row) => (
          <div key={row.label} className="flex w-full items-center gap-2">
            {row.color && <div className="h-2 w-2 shrink-0 rounded-full" style={{ backgroundColor: row.color }} />}
            <div className="flex flex-1 items-center justify-between gap-4 leading-none">
              <span className="text-muted-foreground">{row.label}</span>
              <span className="font-medium text-foreground tabular-nums">{row.value}</span>
            </div>
          </div>
        ))}
      </div>
    </div>,
    document.body
  )
}
