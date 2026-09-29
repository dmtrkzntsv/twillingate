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
  return (
    <div data-scale-legend className="flex items-center justify-end gap-1.5 text-[10px] text-muted-foreground tabular-nums">
      <span>{min}</span>
      <span className="flex gap-0.5">
        {[0, 0.25, 0.5, 0.75, 1].map((t) => (
          <span key={t} className="size-2.5 rounded-[3px]" style={{ backgroundColor: ramp(t, base) }} />
        ))}
      </span>
      <span>{max}</span>
    </div>
  )
}
