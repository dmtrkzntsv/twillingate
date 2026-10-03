import { useQueries } from '@tanstack/react-query'
import type { Widget, WidgetDataQuery } from '@/lib/api'
import { canRefresh, isRemoteTable, widgetQuery } from '@/lib/widget-query'
import { useNow } from './use-now'

export interface Freshness {
  /** The oldest `cached_at` among the widgets on screen. */
  asOf?: Date
  /** The widgets past their `refresh_after`. */
  refreshable: Widget[]
}

/**
 * How old the data on screen is, read from the same queries the cards run
 * (TanStack shares them, so this adds no requests).
 */
export function useFreshness(widgets: Widget[], paramsFor: (w: Widget) => WidgetDataQuery, enabled: boolean): Freshness {
  const now = useNow()
  return useQueries({
    queries: widgets.map((w) => {
      const options = widgetQuery(w, paramsFor(w))
      // A remote table's card asks for its own view; this only reads its first,
      // unfiltered page when that is the one on screen, and never fetches one.
      return { ...options, enabled: enabled && options.enabled && !isRemoteTable(w) }
    }),
    combine: (results) => {
      let oldest: number | undefined
      const refreshable: Widget[] = []
      results.forEach((r, i) => {
        const cachedAt = r.data?.cached_at ? Date.parse(r.data.cached_at) : NaN
        if (!Number.isNaN(cachedAt) && (oldest === undefined || cachedAt < oldest)) oldest = cachedAt
        if (canRefresh(r.data, now)) refreshable.push(widgets[i])
      })
      return { asOf: oldest === undefined ? undefined : new Date(oldest), refreshable }
    },
  })
}
