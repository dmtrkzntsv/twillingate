import { useId, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { definitionChanged, draftErrors, previewBody, type AlertDraft } from '@/lib/alert-draft'
import { definitionKey } from '@/lib/alerts'
import { endpoints, type Alert } from '@/lib/api'
import { alertPreviewQuery } from '@/lib/queries'
import { BacktestAnswer } from './BacktestResult'

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
      <BacktestAnswer alert={alert} query={saved} />
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
          <BacktestAnswer alert={alert} query={mine} />
        </section>
      )}
    </section>
  )
}
