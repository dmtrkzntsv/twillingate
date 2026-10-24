import { queryOptions, replaceEqualDeep } from '@tanstack/react-query'
import { endpoints, type DashboardDetail, type RangeQuery, type ShareState, type SubmissionsQuery } from './api'

/** The dashboards list: the sidebar, the timezone, dev mode. */
export const dashboardsQuery = { queryKey: ['dashboards'], queryFn: () => endpoints.dashboards() }

/** One dashboard with its widgets and its stored selection. */
export const dashboardQuery = (id: number) => ({
  queryKey: ['dashboard', id],
  queryFn: () => endpoints.dashboard(id),
  structuralSharing: shareWidgetsById,
})

/**
 * TanStack's structural sharing, with the widgets matched by id rather than
 * by place. A drop reorders the array, and sharing by place would hand
 * every widget that shifted a new object, equal as it is, which its card's
 * memo takes for a change (D11).
 */
function shareWidgetsById(old: unknown, next: unknown): unknown {
  const shared = replaceEqualDeep(old, next)
  if (shared === old || !isDetail(old) || !isDetail(shared)) return shared
  const before = new Map(old.widgets.map((w) => [w.widget_id, w]))
  const widgets = shared.widgets.map((w) => {
    const prev = before.get(w.widget_id)
    return prev !== undefined && replaceEqualDeep(prev, w) === prev ? prev : w
  })
  return widgets.every((w, i) => w === shared.widgets[i]) ? shared : { ...shared, widgets }
}

function isDetail(data: unknown): data is DashboardDetail {
  return typeof data === 'object' && data !== null && Array.isArray((data as DashboardDetail).widgets)
}

/** Each live project's newest day with data and its unread form submissions (project landing D5, D6). */
export const projectActivityQuery = {
  queryKey: ['project-activity'],
  queryFn: () => endpoints.projectActivity(),
  staleTime: 60_000,
  refetchInterval: 60_000,
}

/** A project page's tabs, in order. */
export const projectTabsQuery = (projectId: number) => ({
  queryKey: ['project-tabs', projectId],
  queryFn: () => endpoints.projectTabs(projectId),
})

/**
 * The project switcher's list. Projects change through this app, MCP and
 * the CLI; the project actions invalidate it, so a dashboard switch or a
 * refocus reuses it instead of asking again; a reload still does.
 */
export const projectsQuery = { queryKey: ['projects'], queryFn: () => endpoints.projects(), staleTime: 5 * 60_000 }

/** Ingest keys, of one project or of all of them. */
export const keysQuery = (projectId?: number) => ({
  queryKey: ['keys', projectId ?? 'all'],
  queryFn: () => endpoints.keys(projectId),
})

/**
 * The limits in force and the settings' defaults, which change only with the server's environment, and the raw
 * events held, which the server itself counts at most every five minutes; so a long stale time.
 */
export const limitsQuery = { queryKey: ['limits'], queryFn: () => endpoints.limits(), staleTime: 5 * 60_000 }

/** Per-project usage over a range (the last 30 days when empty). */
export const usageQuery = (q: RangeQuery & { project_id?: number }) => ({
  queryKey: ['usage', q.project_id ?? 'all', q.from ?? '', q.to ?? ''],
  queryFn: () => endpoints.usage({ project_id: q.project_id, from: q.from, to: q.to }),
})

/** A project's attribute keys over a range, busiest first, with their rules. */
export const attributesQuery = (id: number, q: RangeQuery) => ({
  queryKey: ['attributes', id, q.from ?? '', q.to ?? ''],
  queryFn: () => endpoints.listAttributes(id, { from: q.from, to: q.to }),
})

/** A project's event rules: the renames and blocks in force. */
export const eventRulesQuery = (id: number) => ({
  queryKey: ['event-rules', id],
  queryFn: () => endpoints.eventRules(id),
})

/** The event names a project received over a range, busiest first. */
export const eventNamesQuery = (id: number, q: RangeQuery) => ({
  queryKey: ['event-names', id, q.from ?? '', q.to ?? ''],
  queryFn: () => endpoints.eventNames(id, q),
})

/** Widget shares, live or archived, of one widget or of all of them. */
export const widgetSharesQuery = (q: { widget_id?: number; state?: ShareState }) => ({
  queryKey: ['widget-shares', q.state ?? 'all', q.widget_id ?? 'all'] as const,
  queryFn: () => endpoints.widgetShares(q),
})

/** A project's forms, the active ones or the archived ones. */
export const formsQuery = (projectId: number, archived = false) => ({
  queryKey: ['forms', projectId, archived ? 'archived' : 'active'] as const,
  queryFn: () => endpoints.forms(projectId, archived),
})

/** One page (or a column's distinct values) of a form's submissions table. */
export const submissionsQuery = (projectId: number, name: string, q: SubmissionsQuery) => ({
  queryKey: ['submissions', projectId, name, q] as const,
  queryFn: () => endpoints.submissions(projectId, name, q),
})

/** One submission, every stored field and where its visit came from. */
export const submissionQuery = (projectId: number, name: string, id: string) => ({
  queryKey: ['submission', projectId, name, id] as const,
  queryFn: () => endpoints.submission(projectId, name, id),
})

/** A project's submissions with a field value containing `search`, across its active forms. */
export const findSubmissionsQuery = (projectId: number, search: string) => ({
  queryKey: ['find-submissions', projectId, search] as const,
  queryFn: () => endpoints.findSubmissions(projectId, { search }),
})

/**
 * The components and the source types the server takes: sql is among them
 * only when an external SQL engine is configured. It changes only with a
 * restart.
 */
export const componentsQuery = queryOptions({ queryKey: ['components'], queryFn: () => endpoints.components(), staleTime: 10 * 60_000 })

/** The query schema reference, for the SQL editor's completion; it changes only with a release. */
export const schemaViewsQuery = { queryKey: ['schema-views'], queryFn: () => endpoints.schemaViews(), staleTime: Infinity }

/** Every widget, archived ones included, for the Archive page's widget list. */
export const archivedWidgetsQuery = { queryKey: ['widgets'], queryFn: () => endpoints.listWidgets() }
