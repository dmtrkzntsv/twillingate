import { useCallback, useEffect, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { LayoutGridIcon } from 'lucide-react'
import { useSearchParams } from 'react-router'
import DashboardHeader from '@/components/DashboardHeader'
import { AddWidgetButton, WidgetEditingProvider } from '@/components/editor/WidgetEditing'
import { GridSkeleton, Notice, NoWidgets, PageError } from '@/components/PageStates'
import RangeSwitcher from '@/components/RangeSwitcher'
import SelectorSwitcher from '@/components/SelectorSwitcher'
import { Button } from '@/components/ui/button'
import WidgetGrid from '@/components/WidgetGrid'
import { useDashboardRefresh } from '@/hooks/use-dashboard-refresh'
import { useFreshness } from '@/hooks/use-freshness'
import { useSelectorSelection } from '@/hooks/use-selector-selection'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import { useWidgetActions } from '@/hooks/use-widget-actions'
import type { DashboardDetail, DashboardsResponse, Project, ProjectTab, Widget } from '@/lib/api'
import { writeLastTab } from '@/lib/last-tab'
import { rangeParams } from '@/lib/project-tabs'
import { dashboardQuery } from '@/lib/queries'
import { resolve } from '@/lib/ranges'
import type { ShareContext } from '@/lib/share'
import { chooseSelection } from '@/lib/selection'
import { selectorProjects, writeSelection } from '@/lib/selectors'
import { readStoredRange } from '@/lib/stored-range'
import { selectionParams, widgetParams } from '@/lib/view'
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
 * the URL, else the one stored for every page, and the dashboard's
 * selectors, from the URL alone. Nothing here saves the
 * dashboard's view or the stored range. A dashboard that isn't one of the
 * project's tabs says so and offers to add it.
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
  const [url, setURL] = useSearchParams()
  const projectId = project.project_id
  // Opening the project later lands here (project landing D3).
  useEffect(() => writeLastTab(projectId, tab.dashboard_id), [projectId, tab.dashboard_id])
  const sel = chooseSelection(url, readStoredRange() ?? {}, [projectId], {
    project: false,
    range: dashboard.follows_range,
  })
  const range = sel.range
    ? resolve(sel.range, list.timezone, new Date(), sel.from && sel.to ? { from: sel.from, to: sel.to } : undefined)
    : undefined
  const from = range?.from
  const to = range?.to
  const { picked, pick } = useSelectorSelection(dashboard.selectors)
  const paramsFor = useCallback(
    (w: Widget) => widgetParams(w, { projectId }, from && to ? { from, to } : undefined, picked),
    [projectId, from, to, picked]
  )

  // What a card needs to be shared or downloaded: the pinned project and
  // the range shown, else the default 7 days, as on the dashboard page.
  const shareRange = range ?? resolve('7d', list.timezone, new Date())
  const rangeShown = sel.range !== undefined
  const share = useMemo<ShareContext>(
    () => ({
      project: { id: projectId, name: project.name },
      from: shareRange.from,
      to: shareRange.to,
      rangeShown,
      selectors: dashboard.selectors,
      selection: picked.selection,
    }),
    [projectId, project.name, shareRange.from, shareRange.to, rangeShown, dashboard.selectors, picked.selection]
  )
  // A selector's values come from the project pinned here and each project a query widget fixes.
  const valueProjects = useMemo(() => selectorProjects(dashboard.widgets, projectId, []), [dashboard.widgets, projectId])

  // The user's own dashboard is arranged and edited here as on its own
  // page; a project page shows only live dashboards.
  const widgetActions = useWidgetActions()
  const arrange = dashboard.owner === 'user' ? widgetActions : undefined

  const showGrid = dashboard.widgets.length > 0
  const freshness = useFreshness(dashboard.widgets, paramsFor, showGrid)
  const { refreshing, refreshAll, autoRefresh } = useDashboardRefresh(freshness.refreshable, paramsFor, dashboard.group_id, list.auto_refresh_seconds ?? 0, showGrid)

  return (
    <WidgetEditingProvider
      dashboardId={dashboard.dashboard_id}
      enabled={arrange !== undefined}
      defaults={{ projectId, range: { range: sel.range ?? '7d', from: shareRange.from, to: shareRange.to }, timezone: list.timezone, selection: picked.selection }}
      copyTo={list.dashboards}
    >
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
            autoRefresh={autoRefresh}
            renamable={dashboard.owner === 'user'}
          />
        }
      >
        {arrange && <AddWidgetButton />}
        {dashboard.follows_range && sel.range && (
          <RangeSwitcher
            value={{ range: sel.range, from: sel.from, to: sel.to }}
            timezone={list.timezone}
            // The URL only: viewing a project never saves a dashboard's view (D8) or the stored range.
            onChange={(r) => setURL(writeSelection(selectionParams(r), picked.selection))}
          />
        )}
        {dashboard.selectors.map((s) => (
          <SelectorSwitcher
            key={s.name}
            dashboardId={dashboard.dashboard_id}
            selector={s}
            value={picked.selection[s.name] ?? []}
            projectIds={valueProjects}
            from={shareRange.from}
            to={shareRange.to}
            onChange={(values) => pick(s.name, values)}
          />
        ))}
      </DashboardHeader>
      {showGrid ? (
        <WidgetGrid widgets={dashboard.widgets} paramsFor={paramsFor} share={share} arrange={arrange} />
      ) : (
        <NoWidgets action={arrange ? <AddWidgetButton /> : undefined} />
      )}
    </WidgetEditingProvider>
  )
}
