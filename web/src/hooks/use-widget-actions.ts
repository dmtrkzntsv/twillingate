import { useCallback, useMemo } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError, endpoints, type WidgetLayoutBody } from '@/lib/api'

/** A widget's size on the grid: columns out of 12 and 40px rows. */
export interface WidgetSize {
  width: number
  height: number
}

export interface WidgetActions {
  /**
   * Moves widget `id` after widget `after` (0: first). Resolves true when
   * the server took it and false when it did not (after the toast), so a
   * drag can drop its optimistic order at once.
   */
  move(id: number, after: number): Promise<boolean>
  /** Resizes widget `id`; resolves as `move` does. */
  resize(id: number, size: WidgetSize): Promise<boolean>
}

/**
 * The page's own layout writes for the widgets of a user dashboard: each
 * an update_widget with only a place or a size, which runs no query. Both
 * refetch the dashboards shown, so the grid takes the server's layout;
 * a refusal or a dropped connection shows a toast instead of throwing.
 */
export function useWidgetActions(): WidgetActions {
  const client = useQueryClient()
  const save = useCallback(
    async (id: number, body: WidgetLayoutBody) => {
      try {
        await endpoints.updateWidget(id, body)
        return true
      } catch (err) {
        if (err instanceof ApiError) toast.error(err.message)
        else if (err instanceof TypeError) toast.error("Couldn't reach the server")
        else toast.error(err instanceof Error ? err.message : String(err))
        return false
      } finally {
        await client.invalidateQueries({ queryKey: ['dashboard'] })
      }
    },
    [client]
  )
  return useMemo(
    () => ({
      move: (id, after) => save(id, { after }),
      resize: (id, size) => save(id, size),
    }),
    [save]
  )
}
