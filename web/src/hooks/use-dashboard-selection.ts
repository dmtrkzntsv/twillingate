import { useCallback } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router'
import { endpoints, type DashboardDetail, type Project, type SaveViewBody, type Widget, type WidgetDataQuery } from '@/lib/api'
import { dashboardQuery } from '@/lib/queries'
import { resolve } from '@/lib/ranges'
import { chooseSelection, type Selection } from '@/lib/selection'
import { selectionParams, viewBody, widgetParams, withSavedView, type Switchers } from '@/lib/view'

export interface DashboardSelection {
  sel: Selection
  switchers: Switchers
  /**
   * The range the selection names, resolved to days; the default one (7 days)
   * on a dashboard with no range switcher. Widgets follow it only when the
   * dashboard has the switcher, so it is what a share is captioned with.
   */
  range: { from: string; to: string }
  /** What each widget is asked for under the current selection; stable while it holds. */
  paramsFor: (w: Widget) => WidgetDataQuery
  /** Switches the project or range: into the URL, and saved as the dashboard's view (D35). */
  change: (next: Selection) => void
  /** Opens another tab of the group, carrying the selection there and saving it on that tab (D35). */
  openTab: (id: number) => void
}

/**
 * The project and range a dashboard shows (D34, D35): from the URL, else
 * its stored view, else the defaults. While `frozen` (the page is about to
 * show another dashboard) changes are ignored, so nothing is saved on the
 * dashboard that is leaving.
 */
export function useDashboardSelection(
  dashboard: DashboardDetail,
  projects: Project[],
  timezone: string,
  frozen: boolean
): DashboardSelection {
  const [url, setURL] = useSearchParams()
  const navigate = useNavigate()
  const client = useQueryClient()

  const switchers: Switchers = { project: dashboard.follows_project, range: dashboard.follows_range }
  const active = projects.filter((p) => !p.archived).map((p) => p.project_id)
  const archived = projects.filter((p) => p.archived).map((p) => p.project_id)
  const sel = chooseSelection(url, dashboard, active, switchers, archived)
  const range = resolve(
    sel.range ?? '7d',
    timezone,
    new Date(),
    sel.from && sel.to ? { from: sel.from, to: sel.to } : undefined
  )

  const { projectId } = sel
  // Only a dashboard with a range switcher hands its range to its widgets.
  const from = sel.range ? range.from : undefined
  const to = sel.range ? range.to : undefined
  const paramsFor = useCallback(
    (w: Widget) => widgetParams(w, { projectId }, from && to ? { from, to } : undefined),
    [projectId, from, to]
  )

  // The saved view goes straight into the cached dashboard rather than
  // refetching it. A failed save only loses the stored selection: the URL
  // still carries it.
  const save = (id: number, body: SaveViewBody) =>
    endpoints
      .saveView(id, body)
      .then(() => client.setQueryData<DashboardDetail>(dashboardQuery(id).queryKey, (d) => d && withSavedView(d, body)))
      .catch(() => {})

  const change = (next: Selection) => {
    if (frozen) return
    setURL(selectionParams(next))
    void save(dashboard.dashboard_id, viewBody(next, switchers))
  }

  // Saved with only the parts the target tab has switchers for.
  const openTab = (id: number) => {
    if (frozen) return
    navigate({ pathname: `/dashboards/${id}`, search: selectionParams(sel).toString() })
    client
      .ensureQueryData(dashboardQuery(id))
      .then((target) => save(id, viewBody(sel, { project: target.follows_project, range: target.follows_range })))
      .catch(() => {})
  }

  return { sel, switchers, range, paramsFor, change, openTab }
}
