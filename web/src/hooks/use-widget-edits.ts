import { useCallback, useMemo } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError, endpoints, type Widget } from '@/lib/api'
import { addBody, changedFields, type Draft } from '@/lib/widget-draft'

export interface WidgetEdits {
  /** Adds the draft at the end of the dashboard (spec D14); throws a refusal for the editor's banner. */
  add(dashboardId: number, draft: Draft): Promise<void>
  /** Saves what the draft changed, and nothing when it changed nothing; throws a refusal for the editor's banner. */
  update(widget: Widget, draft: Draft): Promise<void>
  /**
   * Retitles the widget, its name untouched (spec D17). A blank or
   * unchanged title sends nothing. Resolves true when saved or unchanged,
   * false after the refusal's toast.
   */
  rename(widget: Widget, title: string): Promise<boolean>
  /** Archives the widget at once and toasts "Widget archived" with Undo (spec D18). */
  archive(widget: Widget): Promise<void>
  /** Brings an archived widget back; resolves false after the refusal's toast. */
  restore(widget: { widget_id: number; dashboard_id: number }): Promise<boolean>
}

function toastError(err: unknown) {
  if (err instanceof ApiError) toast.error(err.message)
  else if (err instanceof TypeError) toast.error("Couldn't reach the server")
  else toast.error(err instanceof Error ? err.message : String(err))
}

/**
 * The page's content writes for a user dashboard's widgets: the editor's
 * add and update, a card's rename, archive and restore. Each refetches
 * the widget's dashboard before it resolves, so a closing editor or title
 * field gives way to the saved widget, not the old one. Add and update
 * throw a refusal (the editor keeps the input and shows it); the others
 * show it in a toast instead, as menus and fields must not see a
 * rejection. Stable across renders: memoized cards read it.
 */
export function useWidgetEdits(): WidgetEdits {
  const client = useQueryClient()
  const refetch = useCallback(
    (dashboardId: number, also: readonly (readonly unknown[])[] = []) =>
      Promise.all([['dashboard', dashboardId], ...also].map((queryKey) => client.invalidateQueries({ queryKey }))).then(() => {}),
    [client]
  )

  const add = useCallback(
    async (dashboardId: number, draft: Draft) => {
      await endpoints.addWidget(dashboardId, addBody(draft))
      // The list counts each dashboard's live widgets.
      await refetch(dashboardId, [['dashboards']])
    },
    [refetch]
  )

  const update = useCallback(
    async (widget: Widget, draft: Draft) => {
      const body = changedFields(widget, draft)
      if (Object.keys(body).length === 0) return
      await endpoints.updateWidget(widget.widget_id, body)
      // The card's cached answers were read with the old query or drawn
      // for the old component; a new title alone leaves them good.
      const answers = body.source || body.component || body.props ? [['widget', widget.widget_id]] : []
      await refetch(widget.dashboard_id, answers)
    },
    [refetch]
  )

  const rename = useCallback(
    async (widget: Widget, title: string) => {
      const next = title.trim()
      if (next === '' || next === (widget.title ?? '')) return true
      try {
        await endpoints.updateWidget(widget.widget_id, { title: next })
      } catch (err) {
        toastError(err)
        return false
      }
      await refetch(widget.dashboard_id)
      return true
    },
    [refetch]
  )

  const restore = useCallback(
    async (widget: { widget_id: number; dashboard_id: number }) => {
      let ok = false
      try {
        await endpoints.restoreWidget(widget.widget_id)
        ok = true
      } catch (err) {
        toastError(err)
      }
      // The Archive page lists archived widgets; the list counts live ones.
      await refetch(widget.dashboard_id, [['widgets'], ['dashboards']])
      return ok
    },
    [refetch]
  )

  const archive = useCallback(
    async (widget: Widget) => {
      try {
        await endpoints.archiveWidget(widget.widget_id)
        toast('Widget archived', { action: { label: 'Undo', onClick: () => void restore(widget) } })
      } catch (err) {
        toastError(err)
      }
      await refetch(widget.dashboard_id, [['widgets'], ['dashboards']])
    },
    [refetch, restore]
  )

  return useMemo(() => ({ add, update, rename, archive, restore }), [add, update, rename, archive, restore])
}
