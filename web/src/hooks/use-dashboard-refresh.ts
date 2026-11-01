import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import type { Widget, WidgetDataQuery } from '@/lib/api'
import { refreshDashboard } from '@/lib/widget-query'
import { useAutoRefresh } from './use-auto-refresh'
import { useSelectionSignal } from './use-selection-signal'
import { useStoredState } from './use-stored-state'

interface DashboardRefresh {
  refreshing: boolean
  /** Refreshes every widget in `refreshable`, with `fresh`. */
  refreshAll: () => void
  /** The menu's auto-refresh switch; undefined when the server has no interval for it. */
  autoRefresh?: { seconds: number; on: boolean; onChange: (on: boolean) => void }
}

/**
 * A dashboard's refreshes, on its own page and on a project's tab alike.
 * Refresh all reloads through one stream, with `fresh` (widget-stream.ts);
 * leaving the selection stops what is still loading.
 *
 * Auto-refresh is the viewer's own choice per dashboard group, kept in this
 * browser and off until turned on; it runs only while `active`. Each reload
 * is an ordinary one, without `fresh`: the interval is at least
 * REPORTING_CACHE_MAX_AGE, so it runs the queries again rather than being
 * served the answers already on screen. The cards' refetches start
 * together, so they go out as one stream.
 */
export function useDashboardRefresh(
  refreshable: Widget[],
  paramsFor: (w: Widget) => WidgetDataQuery,
  groupId: number,
  autoSeconds: number,
  active: boolean
): DashboardRefresh {
  const client = useQueryClient()
  const [refreshing, setRefreshing] = useState(false)
  const selectionSignal = useSelectionSignal(paramsFor)
  const refreshAll = () => {
    setRefreshing(true)
    // Each widget refreshes what its card shows, a remote table its current page.
    void refreshDashboard(client, refreshable, paramsFor, selectionSignal()).finally(() => setRefreshing(false))
  }
  const [autoOn, setAutoOn] = useStoredState(`twillingate.auto_refresh.${groupId}`, (v) => (v === true ? true : null))
  useAutoRefresh(autoOn === true && active, autoSeconds, () => {
    void client.invalidateQueries({ queryKey: ['widget'] })
  })
  const autoRefresh = autoSeconds > 0 ? { seconds: autoSeconds, on: autoOn === true, onChange: (on: boolean) => setAutoOn(on ? true : null) } : undefined
  return { refreshing, refreshAll, autoRefresh }
}
