import { endpoints, type RangeQuery } from './api'

/** The dashboards list: the sidebar, the timezone, dev mode. */
export const dashboardsQuery = { queryKey: ['dashboards'], queryFn: () => endpoints.dashboards() }

/** One dashboard with its widgets and its stored selection. */
export const dashboardQuery = (id: number) => ({
  queryKey: ['dashboard', id],
  queryFn: () => endpoints.dashboard(id),
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

/** The limits in force and the settings' defaults; they change with the server's environment, so a long stale time. */
export const limitsQuery = { queryKey: ['limits'], queryFn: () => endpoints.limits(), staleTime: 5 * 60_000 }

/** Per-project usage over a range (the last 30 days when empty). */
export const usageQuery = (q: RangeQuery & { project_id?: number }) => ({
  queryKey: ['usage', q.project_id ?? 'all', q.from ?? '', q.to ?? ''],
  queryFn: () => endpoints.usage({ project_id: q.project_id, from: q.from, to: q.to }),
})

/** How a project's data meets the caps over a range. */
export const capUsageQuery = (id: number, q: RangeQuery) => ({
  queryKey: ['cap-usage', id, q.from ?? '', q.to ?? ''],
  queryFn: () => endpoints.capUsage(id, q),
})
