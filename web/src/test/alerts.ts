import { vi } from 'vitest'
import { endpoints, type Alert } from '@/lib/api'

/** An alert as list_alerts answers it: a live, ok stat alert counting events, unless `over` says otherwise. */
export function alert(over: Partial<Alert> = {}): Alert {
  return {
    id: 1,
    project_id: 1,
    name: 'Signups',
    kind: 'stat',
    unit: 'count',
    query: { type: 'stat', event: 'signup' },
    read: { metric: 'count' },
    condition: { type: 'threshold', op: 'below', value: 50 },
    window_days: 1,
    min_volume: 0,
    paused: false,
    snoozed_until: '',
    state: 'ok',
    reason: '',
    last_value: 120,
    last_value_text: '120',
    last_evaluated_at: '2026-11-25T06:00:00Z',
    last_day: '2026-11-24',
    check_requested_at: '',
    check_pending: false,
    recent: [],
    created_at: '2026-11-01T12:00:00Z',
    updated_at: '2026-11-01T12:00:00Z',
    archived_at: '',
    ...over,
  }
}

/** A broken alert: its stored definition no longer parses, so it has no kind, unit or query the console can read. */
export function brokenAlert(over: Partial<Alert> = {}): Alert {
  return alert({
    id: 9,
    name: 'Old funnel',
    kind: '',
    unit: '',
    query: null,
    read: {},
    condition: { type: '' } as unknown as Alert['condition'],
    window_days: 0,
    state: 'broken',
    reason: 'A rule renamed checkout to purchase',
    last_value: null,
    last_value_text: '—',
    ...over,
  })
}

/**
 * Answers `endpoints.alerts` from two lists a test may change as it goes, the
 * live alerts and the archived ones, so the refetch after an action reads
 * what the server would then hold.
 */
export function serveAlerts(live: Alert[], archived: Alert[] = []) {
  const lists = { live, archived }
  const spy = vi.spyOn(endpoints, 'alerts').mockImplementation(async (_projectId: number, inArchive = false) => ({
    max_live: 50,
    alerts: inArchive ? lists.archived : lists.live,
  }))
  return { lists, spy }
}
