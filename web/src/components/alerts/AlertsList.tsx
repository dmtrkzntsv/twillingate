import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { PlusIcon } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import LoadError from '@/components/projects/LoadError'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { describeCondition } from '@/lib/alerts'
import type { Alert } from '@/lib/api'
import { alertPath, rangeParams } from '@/lib/project-tabs'
import { alertsQuery } from '@/lib/queries'
import AlertSparkline from './AlertSparkline'
import AlertStateBadge from './AlertStateBadge'
import NewAlertDialog from './NewAlertDialog'

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

/** The New alert button; at the project's live-alert limit it is kept from opening the wizard, with a tooltip saying why. */
function NewAlertButton({ maxLive, full, onClick }: { maxLive: number; full: boolean; onClick: () => void }) {
  const button = (
    // aria-disabled rather than disabled, so it keeps its focus and its tooltip.
    <Button
      type="button"
      size="sm"
      className="aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
      aria-disabled={full || undefined}
      onClick={full ? undefined : onClick}
    >
      <PlusIcon />
      New alert
    </Button>
  )
  if (!full) return button
  return (
    <Tooltip>
      <TooltipTrigger asChild>{button}</TooltipTrigger>
      <TooltipContent>{`A project holds at most ${maxLive} live alerts`}</TooltipContent>
    </Tooltip>
  )
}

/**
 * The Alerts page's list (`/projects/:id/alerts`): the project's live
 * alerts, or its archived ones with `?show=archived`, in the server's
 * order (by name), each row a link to the alert's page keeping the range.
 * The live list (empty or not) has a New alert button, which opens the
 * wizard (NewAlertDialog); an agent over MCP makes alerts too, which the
 * empty list says. An archived project, which the server keeps from
 * new alerts, has no button, and its empty list says to restore it.
 */
export default function AlertsList({ projectId, projectArchived = false }: { projectId: number; projectArchived?: boolean }) {
  const [url] = useSearchParams()
  const archived = url.get('show') === 'archived'
  const q = useQuery(alertsQuery(projectId, archived))
  const [creating, setCreating] = useState(false)
  const search = rangeParams(url).toString()
  const alerts = q.data?.alerts
  const canCreate = !archived && !projectArchived
  const newAlert = q.data && canCreate && (
    <NewAlertButton maxLive={q.data.max_live} full={q.data.alerts.length >= q.data.max_live} onClick={() => setCreating(true)} />
  )

  let body
  if (q.error && !alerts) body = <LoadError what="the alerts" error={q.error} onRetry={() => void q.refetch()} />
  else if (!alerts) body = <Skeleton aria-hidden className="h-24 w-full max-w-4xl" />
  else if (alerts.length === 0 && archived) body = <p className="text-sm text-muted-foreground">Nothing archived</p>
  else if (alerts.length === 0) {
    body = (
      <section aria-label="No alerts yet" className="flex max-w-4xl min-w-0 flex-col items-start gap-2 rounded-lg border p-4">
        <h3 className="text-base font-semibold">No alerts yet</h3>
        <p className="max-w-prose text-sm text-muted-foreground">
          {canCreate
            ? 'Create one from a template, or ask an agent connected over MCP: for example, to alert you when signups drop 30% against the week before.'
            : 'An archived project takes no new alerts: restore it to create one.'}
        </p>
        {newAlert}
      </section>
    )
  } else {
    body = (
      <div className="flex max-w-4xl min-w-0 flex-col gap-2">
        {newAlert && <div className="flex justify-end">{newAlert}</div>}
        <ul aria-label="Alerts" className="flex min-w-0 flex-col">
          {alerts.map((a) => (
            <Row key={a.id} alert={a} to={alertPath(projectId, a.id, search)} />
          ))}
        </ul>
      </div>
    )
  }
  return (
    <>
      {body}
      {/* Outside the branches, so a refetch that turns the empty list into a list leaves an open wizard as it is. */}
      {canCreate && <NewAlertDialog open={creating} onOpenChange={setCreating} projectId={projectId} />}
    </>
  )
}
