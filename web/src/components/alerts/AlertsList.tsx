import { useQuery } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router'
import LoadError from '@/components/projects/LoadError'
import { Skeleton } from '@/components/ui/skeleton'
import { describeCondition } from '@/lib/alerts'
import type { Alert } from '@/lib/api'
import { alertPath, rangeParams } from '@/lib/project-tabs'
import { alertsQuery } from '@/lib/queries'
import AlertSparkline from './AlertSparkline'
import AlertStateBadge from './AlertStateBadge'

/** One alert's row, a link to its page: the name over what it watches (and why a broken one is), then its last value, recent values and state. */
function Row({ alert, to }: { alert: Alert; to: string }) {
  const condition = describeCondition(alert)
  return (
    <li className="border-b last:border-b-0">
      <Link to={to} className="flex min-w-0 items-center gap-3 rounded-md px-1 py-3 hover:bg-accent/50">
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="truncate font-medium">{alert.name}</span>
          {condition && (
            <span data-condition className="truncate text-sm text-muted-foreground">
              {condition}
            </span>
          )}
          {alert.state === 'broken' && alert.reason && <span className="text-sm break-words text-amber-600 dark:text-amber-400">{alert.reason}</span>}
        </span>
        {/* Stacked on phones, so a long value or a snooze's end never pushes the row wider than the screen. */}
        <span className="flex shrink-0 flex-col items-end gap-1 sm:flex-row sm:items-center sm:gap-3">
          <span className="text-sm whitespace-nowrap tabular-nums">{alert.last_value_text}</span>
          <AlertSparkline recent={alert.recent} className="hidden w-24 sm:block" />
          <AlertStateBadge alert={alert} />
        </span>
      </Link>
    </li>
  )
}

/**
 * The Alerts page's list (`/projects/:id/alerts`): the project's live
 * alerts, or its archived ones with `?show=archived`, in the server's
 * order (by name), each row a link to the alert's page keeping the range.
 * Alerts are made by an agent over MCP, which the empty list says.
 */
export default function AlertsList({ projectId }: { projectId: number }) {
  const [url] = useSearchParams()
  const archived = url.get('show') === 'archived'
  const q = useQuery(alertsQuery(projectId, archived))
  const search = rangeParams(url).toString()
  const alerts = q.data?.alerts

  if (q.error && !alerts) return <LoadError what="the alerts" error={q.error} onRetry={() => void q.refetch()} />
  if (!alerts) return <Skeleton aria-hidden className="h-24 w-full max-w-4xl" />
  if (alerts.length === 0) {
    if (archived) return <p className="text-sm text-muted-foreground">Nothing archived</p>
    return (
      <section aria-label="No alerts yet" className="flex max-w-4xl min-w-0 flex-col gap-2 rounded-lg border p-4">
        <h3 className="text-base font-semibold">No alerts yet</h3>
        <p className="max-w-prose text-sm text-muted-foreground">
          Alerts are made by an agent connected over MCP. Ask it, for example, to alert you when signups drop 30% against the week before.
        </p>
      </section>
    )
  }
  return (
    <ul aria-label="Alerts" className="flex max-w-4xl min-w-0 flex-col">
      {alerts.map((a) => (
        <Row key={a.id} alert={a} to={alertPath(projectId, a.id, search)} />
      ))}
    </ul>
  )
}
