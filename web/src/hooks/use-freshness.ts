import { useCallback, useSyncExternalStore } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import type { Widget, WidgetData, WidgetDataQuery } from '@/lib/api'
import { canRefresh, shownQueries } from '@/lib/widget-query'
import { useNow } from './use-now'

export interface Freshness {
  /** The oldest `cached_at` among the widgets on screen. */
  asOf?: Date
  /** The widgets past their `refresh_after`. */
  refreshable: Widget[]
}

/**
 * How old the data on screen is, read from the queries the cards run (with a
 * remote table's view in the key), so this adds no requests and follows
 * whatever page a card shows.
 */
export function useFreshness(widgets: Widget[], paramsFor: (w: Widget) => WidgetDataQuery, enabled: boolean): Freshness {
  const client = useQueryClient()
  const now = useNow()
  const cache = client.getQueryCache()
  const answers = (w: Widget) =>
    enabled ? shownQueries(client, w, paramsFor(w)).map((q) => q.state.data as WidgetData | undefined) : []
  // Re-rendered when an answer on screen changes; the snapshot is a string,
  // so an unrelated cache event leaves it equal and renders nothing.
  useSyncExternalStore(
    useCallback((onChange) => cache.subscribe(onChange), [cache]),
    () => widgets.map((w) => answers(w).map((a) => `${a?.cached_at}/${a?.refresh_after}`).join(',')).join('|')
  )

  let oldest: number | undefined
  const refreshable: Widget[] = []
  for (const w of widgets) {
    const shown = answers(w)
    for (const a of shown) {
      const cachedAt = a?.cached_at ? Date.parse(a.cached_at) : NaN
      if (!Number.isNaN(cachedAt) && (oldest === undefined || cachedAt < oldest)) oldest = cachedAt
    }
    if (shown.some((a) => canRefresh(a, now))) refreshable.push(w)
  }
  return { asOf: oldest === undefined ? undefined : new Date(oldest), refreshable }
}
