import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { CapUsageRow } from '@/lib/api'
import { capUsageQuery } from '@/lib/queries'
import { formatSpan } from '@/lib/units'
import { cn } from '@/lib/utils'

/** Capped first (most capped days first), then by name. */
function order(rows: CapUsageRow[]): CapUsageRow[] {
  return [...rows].sort((a, b) => b.days_capped - a.days_capped || a.dimension.localeCompare(b.dimension))
}

/**
 * Per capped dimension: the busiest day against the cap, days capped, and
 * the share folded into (other), over the page's range. The heading names
 * the range the rows are for; while another range loads, the previous rows
 * stay, dimmed, and the heading says which range is coming.
 */
export default function CapImpactSection({ projectId, range }: { projectId: number; range: { from: string; to: string } }) {
  const { data, error, isError, isPlaceholderData, refetch } = useQuery({ ...capUsageQuery(projectId, range), placeholderData: keepPreviousData })
  return (
    <section aria-label="Cap impact" className="flex flex-col gap-3 rounded-lg border p-4">
      <header>
        <h2 className="text-base font-semibold">
          Cap impact
          {data && !isError && <span className="font-normal text-muted-foreground"> · {formatSpan(data.from, data.to)}</span>}
        </h2>
        {isPlaceholderData && !isError && (
          <p role="status" className="text-sm text-muted-foreground">Loading {formatSpan(range.from, range.to)}…</p>
        )}
        <p className="text-sm text-muted-foreground">
          Days already rolled up keep only the kept values and their (other) rows, so their values stay near the cap.
        </p>
      </header>
      {isError ? (
        <div className="flex flex-col gap-2 text-sm text-muted-foreground">
          <p>Couldn't load cap impact.</p>
          <p>{error.message}</p>
          <Button variant="outline" size="sm" className="self-start" onClick={() => void refetch()}>Retry</Button>
        </div>
      ) : !data ? (
        <Skeleton aria-hidden className="h-40 w-full" />
      ) : data.dimensions.length === 0 ? (
        <p className="text-sm text-muted-foreground">No data in this range.</p>
      ) : (
        // On a phone the Cap column (a server setting, listed under Limits) is
        // left out and the rest wraps, so the table fits without scrolling sideways.
        <Table className={cn('transition-opacity max-sm:[&_td]:px-1.5 max-sm:[&_th]:px-1.5 [&_th]:whitespace-normal', isPlaceholderData && 'opacity-60')}>
          <TableHeader>
            <TableRow>
              <TableHead>Dimension</TableHead><TableHead className="hidden sm:table-cell">Cap</TableHead><TableHead>Busiest day</TableHead>
              <TableHead>Days capped</TableHead><TableHead>Folded</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {order(data.dimensions).map((d) => (
              <TableRow key={`${d.setting}/${d.dimension}`} aria-label={d.dimension} className={cn(d.days_capped > 0 && 'bg-amber-500/10')}>
                <TableCell className={cn('min-w-24 font-medium whitespace-normal', d.days_capped > 0 && 'border-l-2 border-amber-500')}>
                  <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                    <span className="wrap-anywhere">{d.dimension}</span>
                    {d.days_capped > 0 && <Badge variant="outline" className="border-amber-500/50 text-amber-700 dark:text-amber-400">capped</Badge>}
                  </span>
                </TableCell>
                <TableCell className="hidden sm:table-cell">{d.cap === 0 ? 'no cap' : d.cap.toLocaleString()}</TableCell>
                <TableCell className="whitespace-normal">{d.max_values_per_day.toLocaleString()} <span className="block text-xs whitespace-nowrap text-muted-foreground sm:inline">{d.max_day}</span></TableCell>
                <TableCell>{`${d.days_capped} of ${d.days}`}</TableCell>
                <TableCell>
                  {d.folded_share === null ? '—' : (
                    <span className="flex items-center gap-2">
                      <span className="hidden h-1.5 w-16 overflow-hidden rounded bg-muted sm:block">
                        <span className="block h-full bg-amber-500" style={{ width: `${Math.round(d.folded_share * 100)}%` }} />
                      </span>
                      {`${Math.round(d.folded_share * 100)}%`}
                    </span>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </section>
  )
}
