import { useCallback, useMemo } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError, endpoints, type DashboardDetail, type Widget, type WidgetLayoutBody } from '@/lib/api'

/** A widget's size on the grid: columns out of 12 and 40px rows. */
export interface WidgetSize {
  width: number
  height: number
}

export interface WidgetActions {
  /**
   * Moves widget `id` of dashboard `dashboardId` after widget `after` (0:
   * first). Resolves, once that dashboard is refetched, true when the
   * server took it and false when it did not (after the toast), so a drag
   * can drop its optimistic order at once.
   */
  move(dashboardId: number, id: number, after: number): Promise<boolean>
  /**
   * Resizes widget `id` of dashboard `dashboardId`. Resolves, once that
   * dashboard is refetched, with the size the server holds then, or null
   * when it refused (after the toast). The size held is not always the one
   * asked for: another writer may have changed it between the save and the
   * refetch, and the cell must settle on what the server has.
   */
  resize(dashboardId: number, id: number, size: WidgetSize): Promise<WidgetSize | null>
}

/**
 * The page's own layout writes for the widgets of a user dashboard: each
 * an update_widget with only a place or a size, which runs no query. Both
 * refetch the widget's own dashboard (the other cached ones are not
 * touched), so the grid takes the server's layout; a refusal or a dropped
 * connection shows a toast instead of throwing.
 */
export function useWidgetActions(): WidgetActions {
  const client = useQueryClient()
  // The widget as the server holds it once its dashboard is refetched, or
  // null when the write was refused. The refetched one is read from the
  // cache only if a fetch landed after the write: one that failed, or none
  // at all (no page shows that dashboard), leaves what was there before
  // the write, and the write's own answer is newer then.
  const save = useCallback(
    async (dashboardId: number, id: number, body: WidgetLayoutBody): Promise<Widget | null> => {
      const queryKey = ['dashboard', dashboardId]
      let saved: Widget | null = null
      try {
        saved = await endpoints.updateWidget(id, body)
      } catch (err) {
        if (err instanceof ApiError) toast.error(err.message)
        else if (err instanceof TypeError) toast.error("Couldn't reach the server")
        else toast.error(err instanceof Error ? err.message : String(err))
      }
      // Counted after the write, and in the same tick as the invalidation,
      // which cancels a fetch the page's query already has running: any
      // data counted from here on was read after the write.
      const landed = client.getQueryState(queryKey)?.dataUpdateCount ?? 0
      await client.invalidateQueries({ queryKey })
      if (!saved) return null
      const state = client.getQueryState<DashboardDetail>(queryKey)
      if (!state || state.dataUpdateCount === landed) return saved
      return state.data?.widgets.find((w) => w.widget_id === id) ?? saved
    },
    [client]
  )
  return useMemo(
    () => ({
      move: async (dashboardId, id, after) => (await save(dashboardId, id, { after })) !== null,
      resize: async (dashboardId, id, size) => {
        const held = await save(dashboardId, id, size)
        return held && { width: held.width, height: held.height }
      },
    }),
    [save]
  )
}
