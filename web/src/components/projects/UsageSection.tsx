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
import { eventCount, formatAgo } from '@/lib/units'
import MonthlyUsageChart from './MonthlyUsageChart'

/** One family per series key; labels only, the bars take the palette's colors directly. */
const FAMILIES = ['views', 'events', 'measures']
const config: ChartConfig = {
  views: { label: 'Views' },
  events: { label: 'Product events' },
  measures: { label: 'Measures' },
  checks: { label: 'Checks' },
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

/** A chart's subheading, with what it covers beneath. */
function Subheading({ title, hint }: { title: string; hint: string }) {
  return (
    <div className="flex flex-col">
      <h3 className="text-sm font-medium">{title}</h3>
      <p className="text-xs text-muted-foreground">{hint}</p>
    </div>
  )
}

/**
 * A project's events by calendar month (the last six), then per day over
 * the page's range, by family and the availability checks at their weight
 * (QUOTA_CHECK_RATIO), and tiles for the range's total (the weighted checks in it), freshness and
 * the first day kept.
 */
export default function UsageSection({ projectId, range }: { projectId: number; range: { from: string; to: string } }) {
  const { data, error, isError, isPlaceholderData, refetch } = useQuery({
    ...usageQuery({ project_id: projectId, from: range.from, to: range.to }),
    placeholderData: keepPreviousData,
  })
  const s = data?.projects[0]
  // The checks count at their weight (QUOTA_CHECK_RATIO), stacked as on the monthly chart; 0 leaves them out.
  const ratio = data?.quota_check_ratio ?? 1
  const empty = !!s && eventCount(s.totals, ratio) === 0
  const checks = s ? Math.round(s.totals.checks * ratio) : 0
  const keys = ratio > 0 ? [...FAMILIES, 'checks'] : FAMILIES
  // Rounded like the monthly chart and the Events tile, so a count never reads 72.3.
  const rows = s ? s.series.map((d) => ({ ...d, checks: Math.round(d.checks * ratio) })) : []
  const ticks = niceTicks(rows.map((d) => d.views + d.events + d.measures + d.checks))
  return (
    <section aria-label="Usage" className="flex flex-col gap-3 rounded-lg border p-4">
      <h2 className="text-base font-semibold">Usage</h2>
      <Subheading title="By month" hint="The last six calendar months (UTC), this one so far" />
      <MonthlyUsageChart projectId={projectId} />
      <Subheading title="By day" hint="Over the range above" />
      {isError ? (
        <div className="flex flex-col gap-2 text-sm text-muted-foreground">
          <p>Couldn't load usage.</p>
          <p>{error.message}</p>
          <Button variant="outline" size="sm" className="self-start" onClick={() => void refetch()}>Retry</Button>
        </div>
      ) : !s ? (
        <div aria-hidden className="flex flex-col gap-3">
          <Skeleton className="h-56 w-full" />
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            {[0, 1, 2].map((i) => <Skeleton key={i} className="h-20" />)}
          </div>
        </div>
      ) : (
        <div className={cn('flex flex-col gap-3 transition-opacity', isPlaceholderData && 'opacity-60')}>
          {empty ? (
            <p className="text-sm text-muted-foreground">No events in this range.</p>
          ) : (
            <ChartContainer config={config} className="aspect-auto h-56 w-full">
              <BarChart data={rows} margin={{ top: 8, right: 8, bottom: 0, left: 0 }} barCategoryGap="24%">
                <CartesianGrid {...grid} />
                <XAxis dataKey="day" {...axis} interval="preserveStartEnd" minTickGap={16} tickFormatter={formatTick} />
                <YAxis {...valueAxis} ticks={ticks} domain={[ticks[0], ticks[ticks.length - 1]]} tickFormatter={(v) => formatValue(Number(v))} />
                <ChartTooltip cursor={{ fill: 'var(--muted)', opacity: 0.6 }} content={tooltip('number', { heading: 'day', total: true })} />
                {legend()}
                {keys.map((key, i) => (
                  <Bar
                    key={key}
                    dataKey={key}
                    stackId="stack"
                    fill={seriesColor(i)}
                    radius={i === keys.length - 1 ? [4, 4, 0, 0] : 0}
                    maxBarSize={MAX_BAR}
                    stroke="var(--card)"
                    strokeWidth={1.5}
                    isAnimationActive={false}
                  />
                ))}
              </BarChart>
            </ChartContainer>
          )}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <Tile label="Events" value={Math.round(eventCount(s.totals, ratio)).toLocaleString()}
              hint={`${s.totals.views.toLocaleString()} views · ${s.totals.events.toLocaleString()} product · ${s.totals.measures.toLocaleString()} measures${ratio > 0 ? ` · ${checks.toLocaleString()} checks` : ''}`} />
            <Tile label="Last received" value={s.last_received_at ? formatAgo(s.last_received_at) : 'Nothing received yet'} />
            <Tile label="First day" value={s.first_day ?? '—'} hint={s.first_day ? 'the oldest day the event files hold' : 'nothing kept yet'} />
          </div>
        </div>
      )}
    </section>
  )
}
