import { useId, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import LoadError from '@/components/projects/LoadError'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { definitionChanged, draftErrors, previewBody, type AlertDraft } from '@/lib/alert-draft'
import { definitionKey, formatAlertValue, stateLabel } from '@/lib/alerts'
import { endpoints, type Alert, type AlertPreview } from '@/lib/api'
import { alertPreviewQuery } from '@/lib/queries'

const OUTCOME_COLOURS: Record<AlertPreview['days'][number]['outcome'], string> = {
  breached: 'bg-destructive',
  ok: 'bg-emerald-500/40',
  no_data: 'bg-muted-foreground/25',
}

/** A time as this browser's clock reads it: `Nov 20, 2026, 3:00 PM`. */
function local(iso: string): string {
  return new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })
}

/** How often the backtest says the alert would have fired, over the days it read: a metric alert's days as the answer has them (a stat's 90, a funnel's 30), a silence alert's 30. */
function firedText(alert: Alert, preview: AlertPreview): string {
  const span = alert.kind === 'silence' ? 30 : preview.days.length
  const fired = preview.fired
  if (fired === 0) return `Would not have fired in the last ${span} days`
  return `Would have fired ${fired} ${fired === 1 ? 'time' : 'times'} in the last ${span} days`
}

/** One backtest's answer: how often it would have fired, then a metric alert's days as a strip of cells (which shrink to fit a phone) or a silence alert's gaps. */
function Result({ alert, preview }: { alert: Alert; preview: AlertPreview }) {
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <p className="text-sm">{firedText(alert, preview)}</p>
      {alert.kind === 'silence' ? (
        preview.gaps.length > 0 && (
          <ul className="flex flex-col gap-1 text-sm break-words text-muted-foreground">
            {preview.gaps.map((g) => (
              <li key={g.from}>{`${local(g.from)} – ${local(g.to)} · ${formatAlertValue('hours', g.hours)}`}</li>
            ))}
          </ul>
        )
      ) : (
        <ol data-testid="backtest-strip" aria-label="Days" className="flex h-6 w-full min-w-0 gap-px">
          {preview.days.map((d) => (
            <li
              key={d.day}
              title={`${d.day}: ${formatAlertValue(alert.unit, d.value)} — ${stateLabel(d.outcome)}`}
              className={`h-full min-w-0 flex-1 rounded-[1px] ${OUTCOME_COLOURS[d.outcome]}`}
            />
          ))}
        </ol>
      )}
    </div>
  )
}

/** A backtest being read, failed, or answered. */
function Answer({ alert, query }: { alert: Alert; query: { data?: AlertPreview; error: Error | null; refetch: () => unknown } }) {
  if (query.data) return <Result alert={alert} preview={query.data} />
  if (query.error) return <LoadError what="the backtest" error={query.error} onRetry={() => void query.refetch()} />
  return <Skeleton aria-hidden className="h-6 w-full" />
}

interface Props {
  projectId: number
  alert: Alert
  /** The editor's draft, whose unsaved changes to the definition can be previewed; null for none. */
  draft: AlertDraft | null
}

/**
 * **Backtest**: how the saved alert would have fired over the past
 * (`preview_alert` with its id), read when the page opens and again once
 * its definition changes (an edit here, elsewhere or by an agent; not a
 * rename, a pause or a snooze); not for an archived or a broken alert, which the page does not show it for. While
 * the draft changes the definition, **Preview changes** runs the draft
 * (once it has no errors) and shows that answer, **With your changes**,
 * under the saved one, for as long as the draft is the one it ran; a save,
 * a discard or another edit takes it away.
 */
export default function AlertBacktest({ projectId, alert, draft }: Props) {
  const id = useId()
  const runnable = alert.archived_at === '' && alert.kind !== ''
  const saved = useQuery({ ...alertPreviewQuery(projectId, alert.id, definitionKey(alert)), enabled: runnable })
  const changed = runnable && draft !== null && definitionChanged(alert, draft)
  const body = changed ? previewBody(alert, draft) : null
  const bodyKey = body ? JSON.stringify(body) : null
  // The draft body Preview changes last ran: its answer shows while the draft is still that one.
  const [ran, setRan] = useState<string | null>(null)
  const showing = ran !== null && ran === bodyKey
  const mine = useQuery({
    queryKey: ['alert-preview-draft', projectId, alert.id, ran] as const,
    queryFn: () => endpoints.previewAlert(projectId, JSON.parse(ran!)),
    enabled: showing,
    staleTime: 5 * 60_000,
  })
  if (!runnable) return null
  // A name left empty is no reason to keep the definition from a backtest.
  const blocked = draft !== null && Object.keys(draftErrors(alert, draft)).some((k) => k !== 'name')

  return (
    <section aria-labelledby={`${id}-title`} className="flex min-w-0 flex-col gap-3">
      <h3 id={`${id}-title`} className="text-base font-semibold">
        Backtest
      </h3>
      <Answer alert={alert} query={saved} />
      {changed && !showing && (
        <Button type="button" variant="outline" size="sm" className="self-start" disabled={blocked} onClick={() => setRan(bodyKey)}>
          Preview changes
        </Button>
      )}
      {showing && (
        <section aria-labelledby={`${id}-mine`} className="flex min-w-0 flex-col gap-2">
          <h4 id={`${id}-mine`} className="text-sm font-medium">
            With your changes
          </h4>
          <Answer alert={alert} query={mine} />
        </section>
      )}
    </section>
  )
}
