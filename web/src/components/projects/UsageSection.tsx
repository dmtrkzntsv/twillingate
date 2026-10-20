import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Bar, BarChart, CartesianGrid, XAxis, YAxis } from 'recharts'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ChartContainer, ChartTooltip, type ChartConfig } from '@/components/ui/chart'
import { legend, tooltip } from '@/components/chart-parts'
import { axis, formatTick, grid, MAX_BAR, niceTicks, seriesColor, valueAxis } from '@/lib/chart'
import { formatValue } from '@/lib/format'
import { usageQuery } from '@/lib/queries'
import { cn } from '@/lib/utils'
import { formatAgo, formatBytes, formatDay } from '@/lib/units'
import SizeChart from './SizeChart'

/** One family per series key; labels only, the bars take the palette's colors directly. */
const FAMILIES = ['views', 'events', 'measures']
const config: ChartConfig = {
  views: { label: 'Views' },
  events: { label: 'Product events' },
  measures: { label: 'Measures' },
}

function Tile({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="flex flex-col gap-0.5 rounded-lg border p-3">
      <span className="text-xs text-muted-foreground">{label}</span>
      <span className="text-lg font-semibold">{value}</span>
      {hint && <span className="text-xs text-muted-foreground">{hint}</span>}
    </div>
  )
}

/** Events per day by family, the measured file size per day, and tiles for total, freshness, size and the first day kept. */
export default function UsageSection({ projectId, range }: { projectId: number; range: { from: string; to: string } }) {
  const { data, error, isError, isPlaceholderData, refetch } = useQuery({
    ...usageQuery({ project_id: projectId, from: range.from, to: range.to }),
    placeholderData: keepPreviousData,
  })
  const s = data?.projects[0]
  const empty = !!s && s.totals.views + s.totals.events + s.totals.measures === 0
  const ticks = s ? niceTicks(s.series.map((d) => d.views + d.events + d.measures)) : []
  return (
    <section aria-label="Usage" className="flex flex-col gap-3 rounded-lg border p-4">
      <h2 className="text-base font-semibold">Usage</h2>
      {isError ? (
        <div className="flex flex-col gap-2 text-sm text-muted-foreground">
          <p>Couldn't load usage.</p>
          <p>{error.message}</p>
          <Button variant="outline" size="sm" className="self-start" onClick={() => void refetch()}>Retry</Button>
        </div>
      ) : !s ? (
        <div aria-hidden className="flex flex-col gap-3">
          <Skeleton className="h-56 w-full" />
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
            {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-20" />)}
          </div>
        </div>
      ) : (
        <div className={cn('flex flex-col gap-3 transition-opacity', isPlaceholderData && 'opacity-60')}>
          {empty ? (
            <p className="text-sm text-muted-foreground">No events in this range.</p>
          ) : (
            <ChartContainer config={config} className="aspect-auto h-56 w-full">
              <BarChart data={s.series} margin={{ top: 8, right: 8, bottom: 0, left: 0 }} barCategoryGap="24%">
                <CartesianGrid {...grid} />
                <XAxis dataKey="day" {...axis} interval="preserveStartEnd" minTickGap={16} tickFormatter={formatTick} />
                <YAxis {...valueAxis} ticks={ticks} domain={[ticks[0], ticks[ticks.length - 1]]} tickFormatter={(v) => formatValue(Number(v))} />
                <ChartTooltip cursor={{ fill: 'var(--muted)', opacity: 0.6 }} content={tooltip('number', { heading: 'day', total: true })} />
                {legend()}
                {FAMILIES.map((key, i) => (
                  <Bar
                    key={key}
                    dataKey={key}
                    stackId="stack"
                    fill={seriesColor(i)}
                    radius={i === FAMILIES.length - 1 ? [4, 4, 0, 0] : 0}
                    maxBarSize={MAX_BAR}
                    stroke="var(--card)"
                    strokeWidth={1.5}
                    isAnimationActive={false}
                  />
                ))}
              </BarChart>
            </ChartContainer>
          )}
          <div className="flex flex-col gap-1">
            <h3 className="text-sm font-medium">Data size</h3>
            <SizeChart series={s.series} />
          </div>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <Tile label="Events" value={(s.totals.views + s.totals.events + s.totals.measures).toLocaleString()}
              hint={`${s.totals.views.toLocaleString()} views · ${s.totals.events.toLocaleString()} product · ${s.totals.measures.toLocaleString()} measures`} />
            <Tile label="Last received" value={s.last_received_at ? formatAgo(s.last_received_at) : 'Nothing received yet'} />
            <Tile label="Data size" value={s.size ? formatBytes(s.size.total_bytes) : 'Not measured yet'}
              hint={s.size ? `event files, measured ${formatDay(s.size.measured_at)}` : 'measured by the daily pass'} />
            <Tile label="First day" value={s.first_day ?? '—'} hint={s.first_day ? 'the oldest day the event files hold' : 'nothing kept yet'} />
          </div>
        </div>
      )}
    </section>
  )
}
