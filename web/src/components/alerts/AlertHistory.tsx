import { useEffect, useId, useRef } from 'react'
import { useInfiniteQuery, useQueryClient } from '@tanstack/react-query'
import LoadError from '@/components/projects/LoadError'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { formatAlertValue, stateLabel } from '@/lib/alerts'
import type { Alert, AlertEvaluation } from '@/lib/api'
import { alertHistoryQuery } from '@/lib/queries'

const whenFormat = new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', hourCycle: 'h23' })

/** The columns a 360px phone has no room for. */
const WIDE = 'hidden sm:table-cell'

/** An outcome's text colour: red breached, amber broken, green ok, muted without data, as the state badge. */
const OUTCOME_COLOURS: Record<AlertEvaluation['outcome'], string> = {
  ok: 'text-emerald-600 dark:text-emerald-400',
  breached: 'text-destructive',
  broken: 'text-amber-600 dark:text-amber-400',
  no_data: 'text-muted-foreground',
  error: 'text-destructive',
}

/** The days an evaluation read, `<from> to <to>`; a dash for silence, which reads none. */
function days(e: AlertEvaluation): string {
  return e.from && e.to ? e.from + ' to ' + e.to : '—'
}

/**
 * **History**: the alert's evaluations, newest first, a page at a time:
 * when it was checked (the browser's time), the days read, the value,
 * what it was compared with, the volume and the outcome, with the error
 * under a failed one. A check served later (the alert's
 * `last_evaluated_at` moves) is read in.
 */
export default function AlertHistory({ projectId, alert }: { projectId: number; alert: Alert }) {
  const id = useId()
  const client = useQueryClient()
  const query = alertHistoryQuery(projectId, alert.id)
  const history = useInfiniteQuery(query)
  // A served check changes `last_evaluated_at`; the first value is what the page opened with.
  const seen = useRef(alert.last_evaluated_at)
  useEffect(() => {
    if (seen.current === alert.last_evaluated_at) return
    seen.current = alert.last_evaluated_at
    void client.invalidateQueries({ queryKey: query.queryKey })
  }, [alert.last_evaluated_at, client, query.queryKey])

  const rows = history.data?.pages.flatMap((p) => p.evaluations)
  return (
    <section aria-labelledby={id + '-title'} className="flex min-w-0 flex-col gap-3">
      <h3 id={id + '-title'}className="text-base font-semibold">
        History
      </h3>
      {history.error && !history.data ? (
        <LoadError what="the history" error={history.error} onRetry={() => void history.refetch()} />
      ) : !rows ? (
        <Skeleton aria-hidden className="h-24 w-full" />
      ) : rows.length === 0 ? (
        <p className="text-sm text-muted-foreground">Not evaluated yet</p>
      ) : (
        <>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>When</TableHead>
                <TableHead className={WIDE}>Days</TableHead>
                <TableHead>Value</TableHead>
                <TableHead className={WIDE}>Compared with</TableHead>
                <TableHead className={WIDE}>Volume</TableHead>
                <TableHead>Outcome</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((e) => (
                <TableRow key={e.id}>
                  <TableCell className="whitespace-nowrap">{whenFormat.format(new Date(e.at))}</TableCell>
                  <TableCell className={WIDE + ' whitespace-nowrap'}>{days(e)}</TableCell>
                  <TableCell>{formatAlertValue(alert.unit, e.value)}</TableCell>
                  <TableCell className={WIDE}>{formatAlertValue(alert.unit, e.compare)}</TableCell>
                  <TableCell className={WIDE}>{formatAlertValue('count', e.volume)}</TableCell>
                  <TableCell className="whitespace-normal">
                    <span className={OUTCOME_COLOURS[e.outcome]}>{e.outcome === 'error' ? 'Error' : stateLabel(e.outcome)}</span>
                    {e.error && <p className="text-xs break-words text-muted-foreground">{e.error}</p>}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {history.hasNextPage && (
            <Button type="button" variant="outline" size="sm" className="self-start" disabled={history.isFetchingNextPage} onClick={() => void history.fetchNextPage()}>
              Load more
            </Button>
          )}
        </>
      )}
    </section>
  )
}
