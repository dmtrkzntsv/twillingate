import { useCallback } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router'
import { endpoints, type DashboardDetail, type Project, type SaveViewBody, type Widget, type WidgetDataQuery } from '@/lib/api'
import { dashboardQuery } from '@/lib/queries'
import { resolve } from '@/lib/ranges'
import { chooseSelection, type Selection } from '@/lib/selection'
import { writeSelection } from '@/lib/selectors'
import { readStoredRange, writeStoredRange, type StoredRange } from '@/lib/stored-range'
import { selectionParams, viewBody, widgetParams, withSavedView, type Switchers } from '@/lib/view'
import { useSelectorSelection, type SelectorSelection } from './use-selector-selection'

interface DashboardSelection {
  sel: Selection
  switchers: Switchers
  /**
   * The range the selection names, resolved to days; the default one (7 days)
   * on a dashboard with no range switcher. Widgets follow it only when the
   * dashboard has the switcher, so only then does a share's caption name it
   * (shareCaption); without one it still dates the share and names its file.
   */
  range: { from: string; to: string }
  /** What each widget is asked for under the current selection; stable while it holds. */
  paramsFor: (w: Widget) => WidgetDataQuery
  /** Switches the project: into the URL, and saved as the dashboard's view (D35). */
  change: (next: Selection) => void
  /** Switches the range: into the URL, and stored for every page in this browser (stored-range.ts). */
  changeRange: (r: StoredRange) => void
  /** Opens another tab of the group, carrying the project and range there and saving the project on that tab (D35). */
  openTab: (id: number) => void
  /** The values picked of the dashboard's selectors, which the URL alone keeps. */
  picked: SelectorSelection['picked']
  /** Picks a selector's values. */
  pick: SelectorSelection['pick']
}

/**
 * The project and range a dashboard shows (D34, D35): from the URL, else
 * its stored project and the range stored for every page, else the
 * defaults. While `frozen` (the page is about to show another dashboard)
 * changes are ignored, so nothing is saved on the dashboard that is leaving.
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
  const sel = chooseSelection(url, { project_id: dashboard.project_id, ...readStoredRange() }, active, switchers, archived)
  const range = resolve(
    sel.range ?? '7d',
    timezone,
    new Date(),
    sel.from && sel.to ? { from: sel.from, to: sel.to } : undefined
  )

  const { picked, pick } = useSelectorSelection(dashboard.selectors, frozen)
  const { projectId } = sel
  // Only a dashboard with a range switcher hands its range to its widgets.
  const from = sel.range ? range.from : undefined
  const to = sel.range ? range.to : undefined
  const paramsFor = useCallback(
    (w: Widget) => widgetParams(w, { projectId }, from && to ? { from, to } : undefined, picked),
    [projectId, from, to, picked]
  )

  // The saved view goes straight into the cached dashboard rather than
  // refetching it. A failed save only loses the stored project: the URL
  // still carries it.
  const save = (id: number, body: SaveViewBody | null) =>
    body
      ? endpoints
          .saveView(id, body)
          .then(() => client.setQueryData<DashboardDetail>(dashboardQuery(id).queryKey, (d) => d && withSavedView(d, body)))
          .catch(() => {})
      : Promise.resolve()

  const change = (next: Selection) => {
    if (frozen) return
    setURL(writeSelection(selectionParams(next), picked.selection))
    void save(dashboard.dashboard_id, viewBody(next, switchers))
  }

  // The range is one for every page, kept in this browser: only a pick in
  // the range switcher stores it, so switching the project never stores a
  // range that came from the URL.
  const changeRange = (r: StoredRange) => {
    if (frozen) return
    setURL(writeSelection(selectionParams({ ...sel, range: r.range, from: r.from, to: r.to }), picked.selection))
    writeStoredRange(r)
  }

  // The project is saved on the target tab when it has a project switcher.
  // The selectors' values stay behind: each tab declares selectors of its own.
  const openTab = (id: number) => {
    if (frozen) return
    navigate({ pathname: `/dashboards/${id}`, search: selectionParams(sel).toString() })
    client
      .ensureQueryData(dashboardQuery(id))
      .then((target) => save(id, viewBody(sel, { project: target.follows_project, range: target.follows_range })))
      .catch(() => {})
  }

  return { sel, switchers, range, paramsFor, change, changeRange, openTab, picked, pick }
}
