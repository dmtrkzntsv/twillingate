import type { ChartConfig } from '@/components/ui/chart'

/**
 * Builds a shadcn `ChartConfig` for a set of series keys, for the legend
 * and tooltip labels. Only `label`, no `color`: a series value is arbitrary
 * data text (a `pie`/`radial` label, a `line`/`area`/`bar`/`radar` `series`
 * value), not safe as the CSS custom property name `ChartContainer` would
 * derive from a `color` — each shape is colored directly instead, with
 * `seriesColor`.
 */
export function seriesConfig(keys: string[]): ChartConfig {
  return Object.fromEntries(keys.map((key) => [key, { label: key }]))
}

/** The `i`th series' color, in the palette's fixed order. */
export function seriesColor(i: number): string {
  return `var(--chart-${(i % 5) + 1})`
}

/** Every cartesian axis: no tick marks or rule, small muted labels clear of the plot. */
export const axis = { tickLine: false, axisLine: false, tickMargin: 8, fontSize: 11 } as const

/** A value axis sized to its widest label rather than a fixed guess. */
export const valueAxis = { ...axis, width: 'auto' as const }

/**
 * Round ticks for a value axis covering `values` and zero: about `count`
 * steps of 1, 2, 2.5 or 5 × 10ⁿ, so the axis reads 0 / 100 / 200, never
 * 0 / 95 / 190. Pass it as both `ticks` and the `domain` ends.
 */
export function niceTicks(values: number[], count = 4): number[] {
  const finite = values.filter(Number.isFinite)
  const lo = Math.min(0, ...finite)
  const hi = Math.max(0, ...finite)
  if (lo === hi) return [0, 1]
  const rough = (hi - lo) / count
  const magnitude = 10 ** Math.floor(Math.log10(rough))
  const step = [1, 2, 2.5, 5, 10].map((m) => m * magnitude).find((s) => s >= rough)!
  const first = Math.floor(lo / step)
  const last = Math.ceil(hi / step)
  return Array.from({ length: last - first + 1 }, (_, i) => Number(((first + i) * step).toPrecision(12)))
}

/** Each row's sum over `keys`: the heights a stacked chart's axis must reach. */
export function stackTotals(rows: Record<string, unknown>[], keys: string[]): number[] {
  return rows.map((row) => keys.reduce((sum, key) => sum + (Number(row[key]) || 0), 0))
}

/** Every value under `keys`, for an unstacked chart's axis. */
export function valuesOf(rows: Record<string, unknown>[], keys: string[]): number[] {
  return rows.flatMap((row) => keys.map((key) => Number(row[key])))
}

/** The grid behind a cartesian chart: horizontal hairlines only. */
export const grid = { vertical: false } as const

/** A bar's thickest: the band's leftover is air, not ink. */
export const MAX_BAR = 80

/** The dot a hovered line or area point shows: filled, with a ring in the card's color. */
export function activeDot(color: string) {
  return { r: 4, fill: color, stroke: 'var(--card)', strokeWidth: 2 }
}

const DAY = /^\d{4}-\d{2}-\d{2}$/

function dayFormatter(options: Intl.DateTimeFormatOptions) {
  const f = new Intl.DateTimeFormat('en-US', { ...options, timeZone: 'UTC' })
  return (value: unknown): string =>
    typeof value === 'string' && DAY.test(value) ? f.format(new Date(`${value}T00:00:00Z`)) : String(value ?? '')
}

/** An axis tick: a day as `Sep 1`, anything else as it is. */
export const formatTick = dayFormatter({ month: 'short', day: 'numeric' })

/** A tooltip heading: a day as `Sep 1, 2026`, anything else as it is. */
export const formatHeading = dayFormatter({ month: 'short', day: 'numeric', year: 'numeric' })

/**
 * A step of the sequential scale: `t` from 0 (the smallest value) to 1 (the
 * largest), as chart-1 mixed into `base`. The smallest value still shows
 * (15%), so it never reads as "no data".
 */
export function ramp(t: number, base = 'transparent'): string {
  const pct = 15 + Math.max(0, Math.min(1, t)) * 85
  return `color-mix(in oklab, var(--chart-1) ${Number(pct.toFixed(1))}%, ${base})`
}

/** Whether text on a `ramp(t)` fill should be light rather than ink. */
export function onRamp(t: number): boolean {
  return t > 0.55
}
