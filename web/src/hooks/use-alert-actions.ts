import { useCallback, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { snoozeUntil } from '@/lib/alerts'
import { endpoints, type Alert, type AlertPatch, type AlertsResponse } from '@/lib/api'
import { alertsQuery } from '@/lib/queries'
import { toastError } from '@/lib/toast-error'

export interface AlertActions {
  /** Asks for an evaluation now; the answer comes back with `check_pending` true. */
  check(id: number): Promise<Alert | null>
  pause(id: number): Promise<Alert | null>
  resume(id: number): Promise<Alert | null>
  snooze(id: number, choice: '1h' | '1d' | '1w'): Promise<Alert | null>
  wake(id: number): Promise<Alert | null>
  archive(id: number): Promise<Alert | null>
  restore(id: number): Promise<Alert | null>
  /**
   * Saves a patch; a new definition (condition, window, minimum volume or
   * template fields) asks for a check, which the answer's `check_pending`
   * says. With `onRefusal`, a refusal (an ApiError when the server
   * answered) goes to it instead of a toast, so a form can show it.
   */
  update(id: number, patch: AlertPatch, opts?: { onRefusal?: (err: unknown) => void }): Promise<Alert | null>
  /** The `check_requested_at` of the check this hook asked for alert `id` (Check now, or a save that asks for one: a new definition, a resume, a wake), until it is told the check is done or dropped (a pause, a snooze or an archive from here); undefined when none. A check of another request time is not this hook's (another client asked for one since). */
  asked(id: number): string | undefined
  /** Forgets the check asked for alert `id`, once its pending check has cleared: its result said, or found not to be this hook's request. */
  forget(id: number): void
  /** True while an action runs; buttons wait on it. */
  pending: boolean
}

/** What a write does to a check: asks for one, drops a pending one unrun, or neither. */
type CheckEffect = 'asks' | 'drops' | 'none'

/** What a save of `patch` does to a check: a new definition (condition, window, minimum volume or template fields), a resume or a wake asks for one; a pause or a snooze drops a pending one; a rename does neither. */
function patchCheckEffect(patch: AlertPatch): CheckEffect {
  const definition = patch.condition !== undefined || patch.window_days !== undefined || patch.min_volume !== undefined || patch.fields !== undefined
  if (definition || patch.paused === false || patch.snoozed_until === null) return 'asks'
  if (patch.paused === true || (typeof patch.snoozed_until === 'string' && patch.snoozed_until !== '')) return 'drops'
  return 'none'
}

/** `alerts` with `a` in place of the alert with its id, or added in name order (the server's) when it is not there. */
function upsert(alerts: Alert[], a: Alert): Alert[] {
  if (alerts.some((x) => x.id === a.id)) return alerts.map((x) => (x.id === a.id ? a : x))
  const at = alerts.findIndex((x) => x.name > a.name)
  return at < 0 ? [...alerts, a] : [...alerts.slice(0, at), a, ...alerts.slice(at)]
}

/**
 * Every write to one of a project's alerts, each update_alert or its
 * archive, restore or check route. The answered alert goes into the list it
 * now belongs to (the archived one once archived) and out of the other,
 * each only where that list is already loaded, so the page follows at once;
 * then the project's alert lists refetch. A failure toasts its message
 * (or goes to a save's `onRefusal`), refetches the lists too, and resolves
 * null instead of throwing. The page that shows the alert calls
 * this once and hands it down, so a check asked for anywhere on it is the
 * page's to report when it is done (`asked`).
 */
export function useAlertActions(projectId: number): AlertActions {
  const client = useQueryClient()
  const [pending, setPending] = useState(false)
  const askedIds = useRef(new Map<number, string>())

  const write = useCallback(
    (a: Alert) => {
      const archived = a.archived_at !== ''
      client.setQueryData<AlertsResponse>(alertsQuery(projectId, archived).queryKey, (old) => old && { ...old, alerts: upsert(old.alerts, a) })
      client.setQueryData<AlertsResponse>(
        alertsQuery(projectId, !archived).queryKey,
        (old) => old && { ...old, alerts: old.alerts.filter((x) => x.id !== a.id) }
      )
    },
    [client, projectId]
  )

  const run = useCallback(
    async (fn: () => Promise<Alert>, check: CheckEffect = 'none', onRefusal?: (err: unknown) => void): Promise<Alert | null> => {
      setPending(true)
      try {
        // A list GET in flight began before this write: its answer, landing
        // after this one, would put the alert back as it was (a pending
        // check to none, which would stop the polling).
        await client.cancelQueries({ queryKey: ['alerts', projectId] })
        const a = await fn()
        // Marked, with the request it answered, before the cache shows the
        // pending check, so the page never sees it pending without knowing it
        // asked; a write that asks none marks nothing, even when the answer
        // shows one asked elsewhere.
        if (check === 'asks' && a.check_pending) askedIds.current.set(a.id, a.check_requested_at)
        // A pause, a snooze or an archive drops the request unrun, and its
        // end is no check's result to report. Any other answer with none
        // pending leaves the mark to the page, which forgets it quietly when
        // the check ends unserved.
        else if (check === 'drops') askedIds.current.delete(a.id)
        write(a)
        void client.invalidateQueries({ queryKey: ['alerts', projectId] })
        return a
      } catch (err) {
        if (onRefusal) onRefusal(err)
        else toastError(err)
        // A refusal mostly means the page is stale (paused elsewhere, another
        // edit landed first): read the lists again, so it shows what is so.
        void client.invalidateQueries({ queryKey: ['alerts', projectId] })
        return null
      } finally {
        setPending(false)
      }
    },
    [client, projectId, write]
  )

  const asked = useCallback((id: number) => askedIds.current.get(id), [])
  const forget = useCallback((id: number) => void askedIds.current.delete(id), [])

  const save = (id: number, patch: AlertPatch, onRefusal?: (err: unknown) => void) =>
    run(() => endpoints.updateAlert(projectId, id, patch), patchCheckEffect(patch), onRefusal)

  return {
    pending,
    check: (id) => run(() => endpoints.checkAlert(projectId, id), 'asks'),
    pause: (id) => save(id, { paused: true }),
    resume: (id) => save(id, { paused: false }),
    snooze: (id, choice) => save(id, { snoozed_until: snoozeUntil(new Date(), choice) }),
    wake: (id) => save(id, { snoozed_until: null }),
    archive: (id) => run(() => endpoints.archiveAlert(projectId, id), 'drops'),
    restore: (id) => run(() => endpoints.restoreAlert(projectId, id)),
    update: (id, patch, opts) => save(id, patch, opts?.onRefusal),
    asked,
    forget,
  }
}
