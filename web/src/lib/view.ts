import type { SaveViewBody, Widget, WidgetDataQuery } from './api'
import type { Selection } from './selection'

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

/** The body of `PUT …/view`: only the parts the dashboard has switchers for. */
export function viewBody(sel: Selection, switchers: Switchers): SaveViewBody {
  const body: SaveViewBody = {}
  if (switchers.project && sel.projectId !== undefined) body.project_id = sel.projectId
  if (switchers.range && sel.range) {
    body.range = sel.range
    if (sel.range === 'custom') {
      body.from = sel.from
      body.to = sel.to
    }
  }
  return body
}

/** What a widget is asked for: the project and dates only when it follows them. */
export function widgetParams(
  widget: Widget,
  sel: Selection,
  range: { from: string; to: string } | undefined
): WidgetDataQuery {
  const params: WidgetDataQuery = {}
  if (widget.follows_project && sel.projectId !== undefined) params.project_id = sel.projectId
  if (widget.follows_range && range) {
    params.from = range.from
    params.to = range.to
  }
  return params
}
