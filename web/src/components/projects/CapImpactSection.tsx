import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { CapUsageRow } from '@/lib/api'
import { capUsageQuery } from '@/lib/queries'
import { cn } from '@/lib/utils'

/** Capped first (most capped days first), then by name. */
function order(rows: CapUsageRow[]): CapUsageRow[] {
  return [...rows].sort((a, b) => b.days_capped - a.days_capped || a.dimension.localeCompare(b.dimension))
}

/** Per capped dimension: the busiest day against the cap, days capped, and the share folded into (other). */
export default function CapImpactSection({ projectId, range }: { projectId: number; range: { from: string; to: string } }) {
  const { data, isError, refetch } = useQuery(capUsageQuery(projectId, range))
  return (
    <section aria-label="Cap impact" className="flex flex-col gap-3 rounded-lg border p-4">
      <header>
        <h2 className="text-base font-semibold">Cap impact</h2>
        <p className="text-sm text-muted-foreground">
          Days already rolled up keep only the kept values and their (other) rows, so their values stay near the cap.
        </p>
      </header>
      {isError ? (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          Couldn't load cap impact. <Button variant="outline" size="sm" onClick={() => void refetch()}>Retry</Button>
        </div>
      ) : !data ? null : data.dimensions.length === 0 ? (
        <p className="text-sm text-muted-foreground">No data in this range.</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Dimension</TableHead><TableHead>Cap</TableHead><TableHead>Busiest day</TableHead>
              <TableHead>Days capped</TableHead><TableHead>Folded</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {order(data.dimensions).map((d) => (
              <TableRow key={`${d.setting}/${d.dimension}`} aria-label={d.dimension} className={cn(d.days_capped > 0 && 'bg-amber-500/5')}>
                <TableCell className="font-medium">{d.dimension}</TableCell>
                <TableCell>{d.cap === 0 ? 'no cap' : d.cap.toLocaleString()}</TableCell>
                <TableCell>{d.max_values_per_day.toLocaleString()} <span className="text-xs text-muted-foreground">{d.max_day}</span></TableCell>
                <TableCell>{`${d.days_capped} of ${d.days}`}</TableCell>
                <TableCell>
                  {d.folded_share === null ? '—' : (
                    <span className="flex items-center gap-2">
                      <span className="h-1.5 w-16 overflow-hidden rounded bg-muted">
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
