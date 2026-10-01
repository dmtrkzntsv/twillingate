import { useEffect, useState } from 'react'
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { useParams } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import DashboardHeader from '@/components/DashboardHeader'
import DashboardMenu from '@/components/DashboardMenu'
import { DevErrors, GridSkeleton, NoProjects, NoWidgets, PageError, PageLoading } from '@/components/PageStates'
import ProjectSwitcher from '@/components/ProjectSwitcher'
import RangeSwitcher from '@/components/RangeSwitcher'
import ReportTabs from '@/components/ReportTabs'
import { Button } from '@/components/ui/button'
import WidgetGrid from '@/components/WidgetGrid'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import { useDashboardSelection } from '@/hooks/use-dashboard-selection'
import { useDevReload } from '@/hooks/use-dev-reload'
import { useFreshness } from '@/hooks/use-freshness'
import type { DashboardDetail, DashboardsResponse } from '@/lib/api'
import { moveTabBody } from '@/lib/arrange'
import { rememberDashboard } from '@/lib/last-dashboard'
import { dashboardQuery, dashboardsQuery, projectsQuery } from '@/lib/queries'
import { refreshWidget } from '@/lib/widget-query'

/** `/dashboards/:id`: one dashboard in the app shell, as a report tab or a page of its own (D36). */
export default function Dashboard() {
  const id = Number(useParams().id)
  const list = useQuery(dashboardsQuery)
  // Moving to another dashboard keeps the current one on screen, frozen,
  // until the next one has loaded, so the tabs and header do not flash away.
  const detail = useQuery({ ...dashboardQuery(id), placeholderData: keepPreviousData })
  useDevReload(list.data?.dev === true)

  useEffect(() => {
    if (detail.data && !detail.isPlaceholderData && !detail.data.archived_at) rememberDashboard(detail.data.dashboard_id)
  }, [detail.data, detail.isPlaceholderData])

  const error = list.error ?? detail.error
  return (
    <AppShell dashboards={list.data?.dashboards ?? []} currentId={id}>
      {error ? (
        <PageError error={error} onRetry={() => (list.error ? list.refetch() : detail.refetch())} />
      ) : list.data && detail.data ? (
        <DashboardView list={list.data} dashboard={detail.data} frozen={detail.isPlaceholderData} />
      ) : (
        <PageLoading />
      )}
    </AppShell>
  )
}

interface ViewProps {
  list: DashboardsResponse
  dashboard: DashboardDetail
  /**
   * The URL already names another dashboard: this one stays on screen
   * showing what is cached, but loads nothing and saves nothing, since the
   * URL's selection is not meant for it.
   */
  frozen: boolean
}

function DashboardView({ list, dashboard, frozen }: ViewProps) {
  const client = useQueryClient()
  const projects = useQuery({ ...projectsQuery, enabled: dashboard.follows_project })
  const all = projects.data?.projects ?? []
  const { sel, switchers, paramsFor, change, openTab } = useDashboardSelection(dashboard, all, list.timezone, frozen)

  const waiting = switchers.project && !projects.data
  const noProjects = switchers.project && projects.data !== undefined && all.every((p) => p.archived)
  const showGrid = !waiting && !noProjects && dashboard.widgets.length > 0
  const freshness = useFreshness(dashboard.widgets, paramsFor, showGrid && !frozen)
  const [refreshing, setRefreshing] = useState(false)
  const { move, restore } = useDashboardActions()
  // Only a live user dashboard's group is arranged from the page (D11,
  // D14), and never while frozen: the dashboard on screen is being left.
  // Its tabs stay sortable while frozen, only without moves, so the tab
  // list is not rebuilt under the focus of the tab just chosen.
  const userGroup = dashboard.owner === 'user' && !dashboard.archived_at
  const arrangeable = userGroup && !frozen
  const moveTab = async (id: number, to: number) => {
    const body = moveTabBody(dashboard.tabs, id, dashboard.group_id, to)
    return body ? move(id, body) : false
  }

  const refreshAll = () => {
    setRefreshing(true)
    void Promise.allSettled(freshness.refreshable.map((w) => refreshWidget(client, w, paramsFor(w)))).finally(() =>
      setRefreshing(false)
    )
  }

  return (
    <>
      <TopBar>
        {dashboard.tabs.length > 1 ? (
          <ReportTabs
            tabs={dashboard.tabs}
            currentId={dashboard.dashboard_id}
            onSelect={openTab}
            sortable={userGroup}
            onMove={arrangeable ? moveTab : undefined}
          />
        ) : dashboard.owner === 'user' ? (
          <span className="text-sm text-muted-foreground">Yours</span>
        ) : null}
      </TopBar>
      <div
        aria-busy={frozen || undefined}
        className={`mx-auto flex w-full max-w-[1600px] flex-1 flex-col gap-4 p-3 transition-opacity sm:p-4 lg:p-6 ${frozen ? 'opacity-60' : ''}`}
      >
        {list.dev && list.errors && list.errors.length > 0 && <DevErrors errors={list.errors} />}
        {dashboard.archived_at && (
          // `get_dashboard` and widget data still serve an archived
          // dashboard opened by its URL; this line is the only hint on the
          // page itself that it is gone from the sidebar (D18).
          <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border/70 bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
            <span>Archived: not in the sidebar</span>
            <Button
              variant="outline"
              size="sm"
              onClick={() => void restore(dashboard.dashboard_id, dashboard.owner === 'system')}
            >
              Restore
            </Button>
          </div>
        )}
        <DashboardHeader
          title={dashboard.title}
          asOf={showGrid ? freshness.asOf : undefined}
          refreshable={showGrid && !frozen ? freshness.refreshable.length : 0}
          refreshing={refreshing}
          onRefresh={refreshAll}
          menu={arrangeable ? <DashboardMenu dashboard={dashboard} list={list.dashboards} /> : undefined}
        >
          {switchers.project && !noProjects && projects.data && (
            <ProjectSwitcher projects={all} value={sel.projectId} onChange={(projectId) => change({ ...sel, projectId })} />
          )}
          {switchers.range && sel.range && (
            <RangeSwitcher
              value={{ range: sel.range, from: sel.from, to: sel.to }}
              timezone={list.timezone}
              onChange={(r) => change({ ...sel, range: r.range, from: r.from, to: r.to })}
            />
          )}
        </DashboardHeader>
        {waiting ? (
          projects.error ? (
            <PageError error={projects.error} onRetry={() => projects.refetch()} bare />
          ) : (
            <GridSkeleton />
          )
        ) : noProjects ? (
          <NoProjects />
        ) : dashboard.widgets.length === 0 ? (
          <NoWidgets />
        ) : (
          <WidgetGrid widgets={dashboard.widgets} paramsFor={paramsFor} idle={frozen} />
        )}
      </div>
    </>
  )
}
