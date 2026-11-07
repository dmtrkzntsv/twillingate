import { useQuery } from '@tanstack/react-query'
import { Bar, BarChart, CartesianGrid, Cell, ReferenceLine, XAxis, YAxis } from 'recharts'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { ChartContainer, ChartTooltip, ChartTooltipContent, type ChartConfig } from '@/components/ui/chart'
import { legend } from '@/components/chart-parts'
import { axis, grid, MAX_BAR, niceTicks, seriesColor, valueAxis } from '@/lib/chart'
import { formatValue } from '@/lib/format'
import { usageQuery } from '@/lib/queries'
import { monthHeading, monthLabel, monthlyTotals, monthsRange, utcToday, type MonthTotals } from '@/lib/usage-months'

/** The daily chart's families, in its colors, then the weighted checks a budget counts. */
const config: ChartConfig = {
  views: { label: 'Views' },
  events: { label: 'Product events' },
  measures: { label: 'Measures' },
  checks: { label: 'Checks' },
}

/** A month past the budget: each family in the warning colour, lighter up the stack so the families still part. */
const OVER_OPACITY = [1, 0.75, 0.55, 0.4]

/** The month on the axis; under the current one "so far", and in the warning colour when it is past the budget. */
function MonthTick({ x, y, payload, months }: { x?: number; y?: number; payload?: { value: string }; months: MonthTotals[] }) {
  const m = months.find((t) => t.month === payload?.value)
  if (!m) return null
  return (
    <text x={x} y={y} textAnchor="middle" fontSize={11} fill={m.over !== null ? 'var(--destructive)' : 'var(--muted-foreground)'}>
      <tspan x={x} dy={8}>{monthLabel(m.month)}</tspan>
      {m.current && <tspan x={x} dy={13}>so far</tspan>}
    </text>
  )
}

/**
 * A project's events by calendar month (UTC), the last six with the
 * current one so far: one usage query from the 1st of the oldest month,
 * added up here. With a budget (EVENTS_MONTHLY_BUDGET), a dashed line at
 * it, the checks stacked at their weight (QUOTA_CHECK_RATIO) when that is
 * above 0, and a month past it in the warning colour, its tooltip saying
 * by how much. Display only: nothing is refused over the budget.
 */
export default function MonthlyUsageChart({ projectId }: { projectId: number }) {
  const today = utcToday()
  const { data, error, isError, refetch } = useQuery(usageQuery({ project_id: projectId, ...monthsRange(today) }))
  if (isError) {
    return (
      <div className="flex flex-col gap-2 text-sm text-muted-foreground">
        <p>Couldn't load the monthly totals.</p>
        <p>{error.message}</p>
        <Button variant="outline" size="sm" className="self-start" onClick={() => void refetch()}>Retry</Button>
      </div>
    )
  }
  const s = data?.projects[0]
  if (!data || !s) return <Skeleton aria-hidden className="h-56 w-full" />
  const budget = data.events_monthly_budget ?? null
  const months = monthlyTotals(s.series, today, budget, data.quota_check_ratio ?? 1)
  const keys = ['views', 'events', 'measures', ...(budget !== null && (data.quota_check_ratio ?? 1) > 0 ? ['checks'] : [])]
  const ticks = niceTicks([...months.map((m) => m.total), ...(budget !== null ? [budget] : [])])

  return (
    <ChartContainer config={config} className="aspect-auto h-60 w-full" data-testid="monthly-usage">
      <BarChart data={months} margin={{ top: 8, right: 8, bottom: 0, left: 0 }} barCategoryGap="24%">
        <CartesianGrid {...grid} />
        <XAxis dataKey="month" {...axis} interval={0} height={36} tick={<MonthTick months={months} />} />
        <YAxis {...valueAxis} ticks={ticks} domain={[ticks[0], ticks[ticks.length - 1]]} tickFormatter={(v) => formatValue(Number(v))} />
        <ChartTooltip
          cursor={{ fill: 'var(--muted)', opacity: 0.6 }}
          content={
            <ChartTooltipContent
              indicator="dot"
              total
              valueFormatter={(v) => formatValue(Math.round(v))}
              labelFormatter={(_, payload) => {
                const m = payload?.[0]?.payload as MonthTotals | undefined
                if (!m) return null
                return (
                  <div className="flex flex-col">
                    <span>{monthHeading(m.month)}{m.current ? ', so far' : ''}</span>
                    {m.over !== null && budget !== null && (
                      <span className="font-normal text-destructive">
                        {formatValue(Math.round(m.over))} over the budget of {formatValue(budget)} ({Math.round((m.total / budget) * 100)}%)
                      </span>
                    )}
                  </div>
                )
              }}
            />
          }
        />
        {legend()}
        {budget !== null && (
          <ReferenceLine
            y={budget}
            stroke="var(--muted-foreground)"
            strokeDasharray="4 4"
            ifOverflow="extendDomain"
            label={{ value: `Budget ${formatValue(budget)}`, position: 'insideTopLeft', fontSize: 11, fill: 'var(--muted-foreground)' }}
          />
        )}
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
          >
            {months.map((m) => (
              <Cell
                key={m.month}
                data-over={m.over !== null ? '' : undefined}
                fill={m.over !== null ? 'var(--destructive)' : seriesColor(i)}
                fillOpacity={m.over !== null ? OVER_OPACITY[i] : 1}
              />
            ))}
          </Bar>
        ))}
      </BarChart>
    </ChartContainer>
  )
}
