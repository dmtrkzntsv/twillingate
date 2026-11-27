import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import NotFound from '@/components/NotFound'
import LoadError from '@/components/projects/LoadError'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { useAlertActions } from '@/hooks/use-alert-actions'
import { useNow } from '@/hooks/use-now'
import { addEdits, draftWith, type DraftEdits } from '@/lib/alert-draft'
import { describeCondition, stateLabel } from '@/lib/alerts'
import { endpoints, type Alert, type Project } from '@/lib/api'
import { ALERTS_ID, rangeParams, tabPath } from '@/lib/project-tabs'
import { alertsQuery } from '@/lib/queries'
import { formatAgo } from '@/lib/units'
import AlertActions from './AlertActions'
import AlertBacktest from './AlertBacktest'
import AlertDefinition from './AlertDefinition'
import AlertEditor from './AlertEditor'
import AlertHistory from './AlertHistory'
import AlertStateBadge from './AlertStateBadge'

/** How often the alert lists are read while the alert's check is pending: the runner serves one within a minute. */
const CHECK_POLL_MS = 2_000
const DAY_MS = 86_400_000
/** The longest delay setTimeout takes (a 32-bit count of milliseconds); a longer wait is made in steps. */
const MAX_TIMEOUT_MS = 2_147_483_647
/** How long after a snooze ends the lists are read, so the server's clock is past it. */
const SNOOZE_END_GRACE_MS = 1_000

/** The first day of a window of `days` days that ends on `lastDay` (YYYY-MM-DD, UTC). */
function firstDay(lastDay: string, days: number): string {
  return new Date(Date.parse(`${lastDay}T00:00:00Z`) - (days - 1) * DAY_MS).toISOString().slice(0, 10)
}

/** The last value as the facts line opens: a silence alert's own text (`last event 7h ago`) capitalised, any other's (or a silence alert's none) after `Last value`. */
function lastValue(a: Alert): string {
  if (a.kind === 'silence' && a.last_value !== null) return a.last_value_text.charAt(0).toUpperCase() + a.last_value_text.slice(1)
  return `Last value ${a.last_value_text}`
}

/** The muted line under the heading: the last value, when it was checked, the days it read, and when it was archived. */
function facts(a: Alert, now: number): string {
  const parts = [lastValue(a)]
  if (a.last_evaluated_at) parts.push(`checked ${formatAgo(a.last_evaluated_at, new Date(now))}`)
  // A silence alert reads hours, not days; a broken one has no window.
  if (a.kind !== 'silence' && a.last_day && a.window_days >= 1) parts.push(`days ${firstDay(a.last_day, a.window_days)} to ${a.last_day}`)
  if (a.archived_at) parts.push(`Archived ${a.archived_at.slice(0, 10)}`)
  return parts.join(' · ')
}

/** An archived alert's settings, read-only: when it fired and the minimum volume it judged by. Restore makes them editable again. */
function ArchivedSettings({ alert }: { alert: Alert }) {
  const condition = describeCondition(alert)
  return (
    <section aria-label="Alert settings" className="flex min-w-0 flex-col gap-2">
      <h3 className="text-base font-semibold">Alert settings</h3>
      {condition && <p className="text-sm break-words">{condition}</p>}
      {alert.min_volume > 0 && <p className="text-sm text-muted-foreground">{`Minimum volume ${alert.min_volume}`}</p>}
    </section>
  )
}

/**
 * Reads the project's alert lists every 2 s while alert `id`'s check is
 * pending, and not otherwise: the function form, so the list's own answer
 * turns it off.
 */
function pollWhilePending(id: number) {
  return (query: { state: { data?: { alerts: Alert[] } } }) =>
    query.state.data?.alerts.some((a) => a.id === id && a.check_pending) ? CHECK_POLL_MS : false
}

/**
 * Toasts a failed check: `Check failed: <why>`, the error of evaluation `at`
 * read from the alert's history (the list answer carries only the outcome),
 * or a bare `Check failed` when that read fails or has no text.
 */
async function toastCheckFailed(projectId: number, id: number, at: string) {
  let why = ''
  try {
    const page = await endpoints.alertHistory(projectId, id, undefined, 1)
    why = page.evaluations.find((e) => e.at === at)?.error ?? ''
  } catch {
    // The failure is still said; the History section says its own read failed.
  }
  toast.error(why ? `Check failed: ${why}` : 'Check failed')
}

/**
 * Toasts the result when the alert's pending check clears, if this page
 * asked for it (`actions.asked`, the request time still the alert's
 * `check_requested_at`) and it was served (`last_evaluated_at` set again):
 * `Check failed: <why>` when its evaluation (the newest of `recent`) is an
 * error, which leaves the state as it was, else `Checked: <state>`. A check
 * asked for elsewhere (a request another client made since this page's
 * included), one pending when the page opened, or one dropped unrun (a
 * pause, a snooze or an archive from another client, which leave the alert
 * never evaluated) clears quietly.
 */
function useCheckedToast(projectId: number, alert: Alert | undefined, asked: (id: number) => string | undefined, forget: (id: number) => void) {
  const wasPending = useRef(false)
  useEffect(() => {
    if (!alert) return
    if (alert.check_pending) {
      wasPending.current = true
      return
    }
    if (!wasPending.current) return
    wasPending.current = false
    const request = asked(alert.id)
    if (request === undefined) return
    forget(alert.id)
    if (request !== alert.check_requested_at || !alert.last_evaluated_at) return
    const newest = alert.recent[0]
    if (newest?.outcome === 'error') void toastCheckFailed(projectId, alert.id, newest.at)
    else toast(`Checked: ${stateLabel(alert.state)}`)
  }, [projectId, alert, asked, forget])
}

/**
 * One alert's page (`/projects/:id/alerts/:alertId`), a tab of the Alerts
 * page: its name and state (and why, when broken or without data), a line
 * of facts, the actions, and what it watches. No route reads a single
 * alert, so it is found in the live list, else in the archived one; in
 * neither (purged after archiving, or a wrong link) the page says so,
 * unless a list failed to load, which it says instead, with Retry. While a
 * check is pending the lists are read every 2 s.
 */
export default function AlertPage({ project, alertId }: { project: Project; alertId: string }) {
  const projectId = project.project_id
  const valid = /^\d+$/.test(alertId)
  const id = Number(alertId)
  const [url] = useSearchParams()
  const now = useNow(60_000)
  const actions = useAlertActions(projectId)
  const live = useQuery({ ...alertsQuery(projectId, false), enabled: valid, refetchInterval: pollWhilePending(id) })
  const inLive = live.data?.alerts.find((a) => a.id === id)
  // The archived list is read only once the live one is known to lack it.
  const lookArchived = valid && live.data !== undefined && inLive === undefined
  const archived = useQuery({ ...alertsQuery(projectId, true), enabled: lookArchived, refetchInterval: pollWhilePending(id) })
  const alert = inLive ?? (lookArchived ? archived.data?.alerts.find((a) => a.id === id) : undefined)
  useCheckedToast(projectId, alert, actions.asked, actions.forget)
  // The inputs the editor touched (a template's fields key by key), on the
  // alert they were touched on; the rest follow the saved alert, so a
  // refetch that brings another edit shows it rather than a save sending the
  // old value back.
  const [edits, setEdits] = useState<{ id: number; touched: DraftEdits } | null>(null)
  // An archive drops what was typed: the editor is gone, and a restore brings it back as saved.
  const archivedAt = alert?.archived_at
  useEffect(() => setEdits(null), [archivedAt])
  // A snoozed alert left open reads the live list again when the snooze ends, so its badge and Check now follow the server.
  const client = useQueryClient()
  const snoozedUntil = alert && alert.archived_at === '' ? alert.snoozed_until : ''
  useEffect(() => {
    const end = snoozedUntil ? Date.parse(snoozedUntil) + SNOOZE_END_GRACE_MS : NaN
    if (!(end > Date.now())) return
    let timer: ReturnType<typeof setTimeout>
    const arm = () => {
      const left = end - Date.now()
      if (left <= 0) void client.invalidateQueries({ queryKey: alertsQuery(projectId, false).queryKey })
      else timer = setTimeout(arm, Math.min(left, MAX_TIMEOUT_MS))
    }
    arm()
    return () => clearTimeout(timer)
  }, [snoozedUntil, projectId, client])

  if (!alert) {
    // A list that failed may lack the alert only for being old (a cached one whose reading afresh failed): no telling it is gone.
    if (valid && live.error) return <LoadError what="the alert" error={live.error} onRetry={() => void live.refetch()} />
    if (lookArchived && archived.error) return <LoadError what="the alert" error={archived.error} onRetry={() => void archived.refetch()} />
    // Either list still on its way (a cached one being read afresh included) may yet hold it.
    if (valid && (!live.data || !archived.data || live.isFetching || archived.isFetching)) return <Skeleton aria-hidden className="h-24 w-full max-w-4xl" />
    return (
      <NotFound
        inPage
        title="No alert at this address"
        actions={
          <Button asChild variant="outline">
            <Link to={tabPath(projectId, ALERTS_ID, rangeParams(url).toString())}>Go to the alerts</Link>
          </Button>
        }
      >
        It may have been purged after it was archived, or the link may be wrong.
      </NotFound>
    )
  }

  const draft = draftWith(alert, edits?.id === alert.id ? edits.touched : null)
  const isArchived = alert.archived_at !== ''
  const showReason = (alert.state === 'broken' || alert.state === 'no_data') && alert.reason
  return (
    <div className="grid max-w-4xl min-w-0 grid-cols-1 gap-4">
      <header className="flex min-w-0 flex-col gap-1">
        <div className="flex min-w-0 items-center gap-2">
          <h2 className="min-w-0 truncate text-lg font-semibold">{alert.name}</h2>
          <span className="shrink-0">
            <AlertStateBadge alert={alert} />
          </span>
        </div>
        {showReason && (
          <p className={`text-sm break-words ${alert.state === 'broken' ? 'text-amber-600 dark:text-amber-400' : 'text-muted-foreground'}`}>{alert.reason}</p>
        )}
      </header>
      <p data-testid="alert-facts" className="text-sm break-words text-muted-foreground">
        {facts(alert, now)}
      </p>
      <AlertActions alert={alert} actions={actions} projectArchived={Boolean(project.archived)} />
      <AlertDefinition alert={alert} />
      {isArchived ? (
        <ArchivedSettings alert={alert} />
      ) : (
        <AlertEditor
          key={alert.id}
          projectId={projectId}
          alert={alert}
          actions={actions}
          draft={draft}
          onEdit={(patch) => setEdits((e) => ({ id: alert.id, touched: addEdits(e?.id === alert.id ? e.touched : {}, patch) }))}
          onReset={() => setEdits(null)}
        />
      )}
      {!isArchived && alert.kind !== '' && <AlertBacktest projectId={projectId} alert={alert} draft={draft} />}
      <AlertHistory projectId={projectId} alert={alert} />
    </div>
  )
}
