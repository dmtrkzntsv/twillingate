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
  /** Copies the widget right after itself, on its own dashboard, and toasts "Widget duplicated". */
  duplicate(widget: Widget): Promise<void>
  /**
   * Copies the widget, a built-in one included, to the end of user
   * dashboard `dashboardId`. Resolves true when the server took it, false
   * after the refusal's toast.
   */
  copy(widget: Widget, dashboardId: number): Promise<boolean>
  /**
   * Makes a user dashboard of its own group titled `title` and copies the
   * widget onto it. Resolves with the new dashboard's id, or null after the
   * refusal's toast (the dashboard stays when only the copy was refused).
   */
  copyToNew(widget: Widget, title: string): Promise<number | null>
}

function toastError(err: unknown) {
  if (err instanceof ApiError) toast.error(err.message)
  else if (err instanceof TypeError) toast.error("Couldn't reach the server")
  else toast.error(err instanceof Error ? err.message : String(err))
}

/**
 * The page's content writes for a user dashboard's widgets: the editor's
 * add and update, a card's rename, archive, restore and duplicate; and the
 * copies onto another dashboard, from a built-in one too. Each refetches
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

  const copy = useCallback(
    async (widget: Widget, dashboardId: number) => {
      try {
        await endpoints.copyWidget(widget.widget_id, { dashboard_id: dashboardId })
      } catch (err) {
        toastError(err)
        return false
      }
      // The list counts each dashboard's live widgets.
      await refetch(dashboardId, [['dashboards'], ['widgets']])
      return true
    },
    [refetch]
  )

  const duplicate = useCallback(
    async (widget: Widget) => {
      try {
        await endpoints.copyWidget(widget.widget_id, { dashboard_id: widget.dashboard_id, after: widget.widget_id })
        toast('Widget duplicated')
      } catch (err) {
        toastError(err)
      }
      await refetch(widget.dashboard_id, [['dashboards'], ['widgets']])
    },
    [refetch]
  )

  const copyToNew = useCallback(
    async (widget: Widget, title: string) => {
      let created: number
      try {
        created = (await endpoints.createDashboard({ title })).dashboard_id
      } catch (err) {
        toastError(err)
        return null
      }
      if (await copy(widget, created)) return created
      // The new dashboard stays, empty: the sidebar still has to show it.
      await refetch(created, [['dashboards']])
      return null
    },
    [copy, refetch]
  )

  return useMemo(
    () => ({ add, update, rename, archive, restore, duplicate, copy, copyToNew }),
    [add, update, rename, archive, restore, duplicate, copy, copyToNew]
  )
}
