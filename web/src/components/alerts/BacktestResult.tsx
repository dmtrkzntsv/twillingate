import LoadError from '@/components/projects/LoadError'
import { Skeleton } from '@/components/ui/skeleton'
import { formatAlertValue, stateLabel } from '@/lib/alerts'
import type { Alert, AlertPreview } from '@/lib/api'

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
export function BacktestResult({ alert, preview }: { alert: Alert; preview: AlertPreview }) {
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
export function BacktestAnswer({ alert, query }: { alert: Alert; query: { data?: AlertPreview; error: Error | null; refetch: () => unknown } }) {
  if (query.data) return <BacktestResult alert={alert} preview={query.data} />
  if (query.error) return <LoadError what="the backtest" error={query.error} onRetry={() => void query.refetch()} />
  return <Skeleton aria-hidden className="h-6 w-full" />
}
