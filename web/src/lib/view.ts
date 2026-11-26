import type { DashboardDetail, SaveViewBody, Widget, WidgetDataQuery } from './api'
import type { Selection } from './selection'
import { widgetSelectors, type Selection as Picked } from './selectors'

/** Which switchers a dashboard has (D5): the parts of a selection it takes. */
export interface Switchers {
  project: boolean
  range: boolean
}

/** A selection as URL query parameters (D34): `project`, `range`, and `from`/`to` for custom. */
export function selectionParams(sel: Selection): URLSearchParams {
  const params = new URLSearchParams()
  if (sel.projectId !== undefined) params.set('project', String(sel.projectId))
  if (sel.range) params.set('range', sel.range)
  if (sel.range === 'custom' && sel.from && sel.to) {
    params.set('from', sel.from)
    params.set('to', sel.to)
  }
  return params
}

/**
 * The body of `PUT …/view`: the project, which only a dashboard with a
 * project switcher stores; null when there is nothing to store. The range
 * is never stored on the server (stored-range.ts).
 */
export function viewBody(sel: Selection, switchers: Switchers): SaveViewBody | null {
  return switchers.project && sel.projectId !== undefined ? { project_id: sel.projectId } : null
}

/**
 * What a widget is asked for: the project and dates only when it follows
 * them, and the values picked of the selectors it follows (`picked`, with
 * `order` the dashboard's selector names) only when there are any.
 */
export function widgetParams(
  widget: Widget,
  sel: Selection,
  range: { from: string; to: string } | undefined,
  picked?: { selection: Picked; order: string[] }
): WidgetDataQuery {
  const params: WidgetDataQuery = {}
  if (widget.follows_project && sel.projectId !== undefined) params.project_id = sel.projectId
  if (widget.follows_range && range) {
    params.from = range.from
    params.to = range.to
  }
  const selectors = picked && widgetSelectors(widget, picked.selection, picked.order)
  if (selectors) params.selectors = selectors
  return params
}

/** A dashboard as it reads once `body` is saved as its view: what the server now stores. */
export function withSavedView(dashboard: DashboardDetail, body: SaveViewBody): DashboardDetail {
  return { ...dashboard, project_id: body.project_id }
}
