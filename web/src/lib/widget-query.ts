import { keepPreviousData, queryOptions, type QueryClient } from '@tanstack/react-query'
import { widgets } from '@/components/widgets'
import type { WidgetModule } from '@/components/widgets/types'
import { endpoints, type Widget, type WidgetData, type WidgetDataQuery } from './api'
import { liveFilters, type TableView } from './table-view'

/** The widget's component, if the bundle has it: a null or unknown name means it was removed. */
export function componentOf(widget: Widget): WidgetModule | undefined {
  return widget.component !== null && Object.hasOwn(widgets, widget.component) ? widgets[widget.component] : undefined
}

/** A table whose filters, sort and page the server applies; every other widget refuses them. */
export function isRemoteTable(widget: Widget): boolean {
  return widget.component === 'table' && widget.props.mode === 'remote'
}

/**
 * A remote table's view as request arguments, each left out at its default.
 * Once an answer has named the columns, a filter or sort on a column it
 * lacks is kept in the view but not sent (the server would refuse it);
 * before one has, the view is sent whole.
 */
export function viewQuery(widget: Widget, view: TableView, columns: string[] | undefined): Partial<WidgetDataQuery> {
  if (!isRemoteTable(widget)) return {}
  const filters = columns ? liveFilters(view, columns) : view.filters
  const sort = view.sort && (!columns || columns.includes(view.sort.column)) ? view.sort : null
  const q: Partial<WidgetDataQuery> = {}
  if (filters.length > 0) q.filters = JSON.stringify(filters)
  if (sort) q.sort = `${sort.column}:${sort.dir}`
  if (view.offset > 0) q.offset = view.offset
  return q
}

/**
 * One widget's data as a TanStack query (D38), keyed by what changes the
 * answer: the widget, the project, the range and a remote table's view.
 * `fresh` is not part of the key: a refresh replaces the cached answer
 * under the same key.
 */
export function widgetQuery(widget: Widget, params: WidgetDataQuery, idle = false, view: Partial<WidgetDataQuery> = {}) {
  return queryOptions({
    queryKey: [
      'widget',
      widget.widget_id,
      params.project_id,
      params.from,
      params.to,
      view.filters,
      view.sort,
      view.offset,
    ] as const,
    queryFn: () => endpoints.widgetData(widget.widget_id, { ...params, ...view }),
    // A widget whose component is gone has nothing to render the data with;
    // an idle one keeps showing what is cached but asks for nothing new.
    enabled: !idle && componentOf(widget) !== undefined,
    staleTime: 60_000,
    // A remote table keeps its current page on screen while the next one loads.
    placeholderData: isRemoteTable(widget) ? keepPreviousData : undefined,
  })
}

/** Refetches a widget with `fresh=true`, bypassing the server's cache (D39). */
export function refreshWidget(
  client: QueryClient,
  widget: Widget,
  params: WidgetDataQuery,
  view: Partial<WidgetDataQuery> = {}
): Promise<WidgetData> {
  const { queryKey } = widgetQuery(widget, params, false, view)
  return client.fetchQuery({
    queryKey,
    queryFn: () => endpoints.widgetData(widget.widget_id, { ...params, ...view, fresh: true }),
    staleTime: 0,
  })
}

/**
 * The view arguments of every query on screen for this widget and selection:
 * a remote table's current page, or `{}` for any other widget. The view lives
 * in the card, so a refresh from outside it reads the view back from the key.
 */
export function shownViews(client: QueryClient, widget: Widget, params: WidgetDataQuery): Partial<WidgetDataQuery>[] {
  const prefix = widgetQuery(widget, params).queryKey.slice(0, 5)
  const views = client
    .getQueryCache()
    .findAll({ queryKey: prefix, type: 'active' })
    .map(({ queryKey }) => {
      const [, , , , , filters, sort, offset] = queryKey as ReturnType<typeof widgetQuery>['queryKey']
      return { filters, sort, offset }
    })
  return views.length > 0 ? views : [{}]
}

/** Whether a widget's answer may be refreshed yet: cacheable and past `refresh_after`. */
export function canRefresh(data: WidgetData | undefined, now: number): boolean {
  if (!data?.refresh_after || data.source_type !== 'sql') return false
  return Date.parse(data.refresh_after) <= now
}
