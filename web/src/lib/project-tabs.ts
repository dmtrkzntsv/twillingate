import type { DashboardInfo, ProjectTab } from './api'

/** The Setup tab's id in `ReportTabs`; no dashboard has id 0. */
export const SETUP_ID = 0

/** Where a project's tab lives: Setup at `/setup`, a dashboard under `/dashboards/:id` (D8). */
export function tabPath(projectId: number, dashboardId: number, search = ''): string {
  const path = dashboardId === SETUP_ID ? `/projects/${projectId}/setup` : `/projects/${projectId}/dashboards/${dashboardId}`
  return search ? `${path}?${search}` : path
}

/** The part of a project page's URL that every tab shares: the range (D8). */
export function rangeParams(url: URLSearchParams): URLSearchParams {
  const params = new URLSearchParams()
  for (const key of ['range', 'from', 'to']) {
    const v = url.get(key)
    if (v !== null) params.set(key, v)
  }
  return params
}

/**
 * What "+" offers: the built-ins not on the project (hidden from the
 * sidebar or not), and the user's live dashboards not on it, in list order.
 */
export function pickerSections(dashboards: DashboardInfo[], tabs: ProjectTab[]): { builtin: DashboardInfo[]; own: DashboardInfo[] } {
  const taken = new Set(tabs.map((t) => t.dashboard_id))
  const free = dashboards.filter((d) => !taken.has(d.dashboard_id))
  return {
    builtin: free.filter((d) => d.owner === 'system'),
    own: free.filter((d) => d.owner === 'user' && !d.archived_at),
  }
}

/**
 * The `after` that moves the user's tab `id` to index `to` among the
 * user's tabs: 0 for first, else the user tab before that place once `id`
 * is taken out.
 */
export function userAfter(tabs: ProjectTab[], id: number, to: number): number {
  const rest = tabs.filter((t) => t.owner === 'user' && t.dashboard_id !== id)
  return to <= 0 ? 0 : (rest[Math.min(to, rest.length) - 1]?.dashboard_id ?? 0)
}
