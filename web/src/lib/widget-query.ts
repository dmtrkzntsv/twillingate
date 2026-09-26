import { queryOptions, type QueryClient } from '@tanstack/react-query'
import { widgets } from '@/components/widgets'
import type { WidgetModule } from '@/components/widgets/types'
import { endpoints, type Widget, type WidgetData, type WidgetDataQuery } from './api'

/** The widget's component, if the bundle has it: a null or unknown name means it was removed. */
export function componentOf(widget: Widget): WidgetModule | undefined {
  return widget.component !== null && Object.hasOwn(widgets, widget.component) ? widgets[widget.component] : undefined
}

/**
 * One widget's data as a TanStack query (D38), keyed by what changes the
 * answer: the widget, the project and the range. `fresh` is not part of the
 * key: a refresh replaces the cached answer under the same key.
 */
export function widgetQuery(widget: Widget, params: WidgetDataQuery, idle = false) {
  return queryOptions({
    queryKey: ['widget', widget.widget_id, params.project_id, params.from, params.to] as const,
    queryFn: () => endpoints.widgetData(widget.widget_id, params),
    // A widget whose component is gone has nothing to render the data with;
    // an idle one keeps showing what is cached but asks for nothing new.
    enabled: !idle && componentOf(widget) !== undefined,
    staleTime: 60_000,
  })
}

/** Refetches a widget with `fresh=true`, bypassing the server's cache (D39). */
export function refreshWidget(client: QueryClient, widget: Widget, params: WidgetDataQuery): Promise<WidgetData> {
  return client.fetchQuery({
    ...widgetQuery(widget, params),
    queryFn: () => endpoints.widgetData(widget.widget_id, { ...params, fresh: true }),
    staleTime: 0,
  })
}

/** Whether a widget's answer may be refreshed yet: cacheable and past `refresh_after`. */
export function canRefresh(data: WidgetData | undefined, now: number): boolean {
  if (!data?.refresh_after || data.source_type !== 'sql') return false
  return Date.parse(data.refresh_after) <= now
}
