import type { ChartConfig } from '@/components/ui/chart'

/**
 * Builds a shadcn `ChartConfig` for a set of series keys, for the legend
 * and tooltip labels. Only `label`, no `color`: a series value is arbitrary
 * data text (a `pie`/`radial` label, a `line`/`area`/`bar`/`radar` `series`
 * value), not safe as the CSS custom property name `ChartContainer` would
 * derive from a `color` — each shape is colored directly instead, cycling
 * `var(--chart-1..5)`.
 */
export function seriesConfig(keys: string[]): ChartConfig {
  return Object.fromEntries(keys.map((key) => [key, { label: key }]))
}
