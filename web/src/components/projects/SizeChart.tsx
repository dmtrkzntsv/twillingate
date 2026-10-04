import { CartesianGrid, Line, LineChart, XAxis, YAxis } from 'recharts'
import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from '@/components/ui/chart'
import type { UsageDay } from '@/lib/api'
import { activeDot, axis, formatHeading, formatTick, grid, niceTicks, seriesColor, valueAxis } from '@/lib/chart'
import { formatBytes } from '@/lib/units'

const config: ChartConfig = { total_bytes: { label: 'Data size' } }

/** The project's measured size per day; days not measured are bridged, never drawn as zero. */
export default function SizeChart({ series }: { series: UsageDay[] }) {
  const measured = series.flatMap((d) => (d.total_bytes === null ? [] : [d.total_bytes]))
  if (measured.length === 0) return <p className="text-sm text-muted-foreground">No size measured in this range.</p>
  const ticks = niceTicks(measured)
  return (
    <ChartContainer config={config} className="aspect-auto h-36 w-full">
      <LineChart data={series} margin={{ top: 8, right: 8, bottom: 0, left: 0 }}>
        <CartesianGrid {...grid} />
        <XAxis dataKey="day" {...axis} interval="preserveStartEnd" minTickGap={16} tickFormatter={formatTick} />
        <YAxis {...valueAxis} ticks={ticks} domain={[ticks[0], ticks[ticks.length - 1]]} tickFormatter={(v: number) => formatBytes(v)} />
        <ChartTooltip
          cursor={{ strokeWidth: 1 }}
          content={
            <ChartTooltipContent
              indicator="line"
              labelFormatter={(_, payload) => formatHeading(payload?.[0]?.payload?.day)}
              valueFormatter={(v) => formatBytes(v)}
            />
          }
        />
        <Line
          type="monotone"
          dataKey="total_bytes"
          connectNulls
          stroke={seriesColor(0)}
          strokeWidth={2}
          strokeLinecap="round"
          strokeLinejoin="round"
          dot={measured.length === 1 ? { r: 3, fill: seriesColor(0), stroke: 'var(--card)', strokeWidth: 2 } : false}
          activeDot={activeDot(seriesColor(0))}
          isAnimationActive={false}
        />
      </LineChart>
    </ChartContainer>
  )
}
