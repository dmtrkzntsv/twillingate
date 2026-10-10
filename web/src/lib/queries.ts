import { endpoints, type RangeQuery, type ShareState, type SubmissionsQuery } from './api'

/** The dashboards list: the sidebar, the timezone, dev mode. */
export const dashboardsQuery = { queryKey: ['dashboards'], queryFn: () => endpoints.dashboards() }

/** One dashboard with its widgets and its stored selection. */
export const dashboardQuery = (id: number) => ({
  queryKey: ['dashboard', id],
  queryFn: () => endpoints.dashboard(id),
})

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

/** The attribute keys a project (or none: just the budget) received over a range. */
export const receivedAttributesQuery = (q: RangeQuery & { project_id?: number }) => ({
  queryKey: ['received-attributes', q.project_id ?? 'none', q.from ?? '', q.to ?? ''],
  queryFn: () => endpoints.receivedAttributes({ project_id: q.project_id, from: q.from, to: q.to }),
})

/** How a project's data meets the caps over a range. */
export const capUsageQuery = (id: number, q: RangeQuery) => ({
  queryKey: ['cap-usage', id, q.from ?? '', q.to ?? ''],
  queryFn: () => endpoints.capUsage(id, q),
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
