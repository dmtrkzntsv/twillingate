import { endpoints } from './api'

/** The dashboards list: the sidebar, the timezone, dev mode. */
export const dashboardsQuery = { queryKey: ['dashboards'], queryFn: () => endpoints.dashboards() }

/** One dashboard with its widgets and its stored selection. */
export const dashboardQuery = (id: number) => ({
  queryKey: ['dashboard', id],
  queryFn: () => endpoints.dashboard(id),
})

export const projectsQuery = { queryKey: ['projects'], queryFn: () => endpoints.projects() }
