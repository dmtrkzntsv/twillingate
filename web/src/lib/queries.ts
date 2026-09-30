import { endpoints } from './api'

/** The dashboards list: the sidebar, the timezone, dev mode. */
export const dashboardsQuery = { queryKey: ['dashboards'], queryFn: () => endpoints.dashboards() }

/** One dashboard with its widgets and its stored selection. */
export const dashboardQuery = (id: number) => ({
  queryKey: ['dashboard', id],
  queryFn: () => endpoints.dashboard(id),
})

/**
 * The project switcher's list. Projects change through MCP and the CLI,
 * never here, so a dashboard switch or a refocus reuses it instead of
 * asking again; a reload still does.
 */
export const projectsQuery = { queryKey: ['projects'], queryFn: () => endpoints.projects(), staleTime: 5 * 60_000 }
