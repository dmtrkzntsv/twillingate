import { useCallback, useEffect, useMemo, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LayoutGridIcon } from 'lucide-react'
import { useSearchParams } from 'react-router'
import DashboardHeader from '@/components/DashboardHeader'
import { GridSkeleton, Notice, NoWidgets, PageError } from '@/components/PageStates'
import RangeSwitcher from '@/components/RangeSwitcher'
import { Button } from '@/components/ui/button'
import WidgetGrid from '@/components/WidgetGrid'
import { useAutoRefresh } from '@/hooks/use-auto-refresh'
import { useFreshness } from '@/hooks/use-freshness'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import { useStoredState } from '@/hooks/use-stored-state'
import type { DashboardDetail, DashboardsResponse, Project, ProjectTab, Widget } from '@/lib/api'
import { writeLastTab } from '@/lib/last-tab'
import { rangeParams } from '@/lib/project-tabs'
import { dashboardQuery } from '@/lib/queries'
import { resolve } from '@/lib/ranges'
import type { ShareContext } from '@/lib/share'
import { chooseSelection } from '@/lib/selection'
import { selectionParams, widgetParams } from '@/lib/view'
import { refreshWidget, shownViews } from '@/lib/widget-query'
import ProjectTabMenu from './ProjectTabMenu'

interface Props {
  project: Project
  dashId: number
  /** The project's tabs, in order; undefined while they load. */
  tabs?: ProjectTab[]
  /** The dashboards list, for the timezone and the auto-refresh interval; undefined while it loads. */
  list?: DashboardsResponse
  actions: ProjectTabActions
}

/**
 * One of a project's dashboard tabs (project tabs D8): the dashboard with
 * the project pinned, so there is no project switcher, and the range from
 * the URL, else the dashboard's own. Nothing here saves the dashboard's
 * view. A dashboard that isn't one of the project's tabs says so and offers
 * to add it.
 */
export default function ProjectDashboardTab({ project, dashId, tabs, list, actions }: Props) {
  const detail = useQuery(dashboardQuery(dashId))
  if (detail.error) return <PageError error={detail.error} onRetry={() => void detail.refetch()} bare />
  if (!detail.data || !tabs || !list) return <GridSkeleton />
  const tab = tabs.find((t) => t.dashboard_id === dashId)
  if (!tab) {
    return (
      <Notice icon={<LayoutGridIcon />} title={`${detail.data.title} isn't a tab of ${project.name}`}>
        <Button variant="outline" size="sm" disabled={actions.pending} onClick={() => void actions.add(project.project_id, dashId)}>
          Add tab
        </Button>
      </Notice>
    )
  }
  return <TabView key={dashId} project={project} dashboard={detail.data} tab={tab} tabs={tabs} list={list} actions={actions} />
}

interface ViewProps {
  project: Project
  dashboard: DashboardDetail
  tab: ProjectTab
  tabs: ProjectTab[]
  list: DashboardsResponse
  actions: ProjectTabActions
}

function TabView({ project, dashboard, tab, tabs, list, actions }: ViewProps) {
  const client = useQueryClient()
  const [url, setURL] = useSearchParams()
  const projectId = project.project_id
  // Opening the project later lands here (project landing D3).
  useEffect(() => writeLastTab(projectId, tab.dashboard_id), [projectId, tab.dashboard_id])
  const sel = chooseSelection(url, { range: dashboard.range, from: dashboard.from, to: dashboard.to }, [projectId], {
    project: false,
    range: dashboard.follows_range,
  })
  const range = sel.range
    ? resolve(sel.range, list.timezone, new Date(), sel.from && sel.to ? { from: sel.from, to: sel.to } : undefined)
    : undefined
  const from = range?.from
  const to = range?.to
  const paramsFor = useCallback(
    (w: Widget) => widgetParams(w, { projectId }, from && to ? { from, to } : undefined),
    [projectId, from, to]
  )

  // What a card needs to be shared or downloaded: the pinned project and
  // the range shown, else the default 7 days, as on the dashboard page.
  const shareRange = range ?? resolve('7d', list.timezone, new Date())
  const rangeShown = sel.range !== undefined
  const writable = !list.dev
  const share = useMemo<ShareContext>(
    () => ({
      project: { id: projectId, name: project.name },
      from: shareRange.from,
      to: shareRange.to,
      rangeShown,
      writable,
    }),
    [projectId, project.name, shareRange.from, shareRange.to, rangeShown, writable]
  )

  const showGrid = dashboard.widgets.length > 0
  const freshness = useFreshness(dashboard.widgets, paramsFor, showGrid)
  const [refreshing, setRefreshing] = useState(false)
  const refreshAll = () => {
    setRefreshing(true)
    const refreshes = freshness.refreshable.flatMap((w) =>
      shownViews(client, w, paramsFor(w)).map((view) => refreshWidget(client, w, paramsFor(w), view))
    )
    void Promise.allSettled(refreshes).finally(() => setRefreshing(false))
  }

  // The same per-viewer choice as on the dashboard page, kept per group.
  const autoSeconds = list.auto_refresh_seconds ?? 0
  const [autoOn, setAutoOn] = useStoredState(`twillingate.auto_refresh.${dashboard.group_id}`, (v) => (v === true ? true : null))
  useAutoRefresh(autoOn === true && showGrid, autoSeconds, () => {
    void client.invalidateQueries({ queryKey: ['widget'] })
  })

  return (
    <>
      <DashboardHeader
        title={dashboard.title}
        asOf={showGrid ? freshness.asOf : undefined}
        refreshable={showGrid ? freshness.refreshable.length : 0}
        refreshing={refreshing}
        onRefresh={refreshAll}
        menu={
          <ProjectTabMenu
            projectId={projectId}
            tab={tab}
            tabs={tabs}
            actions={actions}
            search={rangeParams(url).toString()}
            autoRefresh={
              autoSeconds > 0
                ? { seconds: autoSeconds, on: autoOn === true, onChange: (on) => setAutoOn(on ? true : null) }
                : undefined
            }
          />
        }
      >
        {dashboard.follows_range && sel.range && (
          <RangeSwitcher
            value={{ range: sel.range, from: sel.from, to: sel.to }}
            timezone={list.timezone}
            // The URL only: viewing a project never saves a dashboard's view (D8).
            onChange={(r) => setURL(selectionParams(r))}
          />
        )}
      </DashboardHeader>
      {showGrid ? <WidgetGrid widgets={dashboard.widgets} paramsFor={paramsFor} share={share} /> : <NoWidgets />}
    </>
  )
}
