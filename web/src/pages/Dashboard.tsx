import { useCallback, useEffect, useState } from 'react'
import { keepPreviousData, useQuery, useQueryClient } from '@tanstack/react-query'
import { FolderPlusIcon, LayoutGridIcon, TriangleAlertIcon } from 'lucide-react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import DashboardHeader from '@/components/DashboardHeader'
import ProjectSwitcher from '@/components/ProjectSwitcher'
import RangeSwitcher from '@/components/RangeSwitcher'
import ReportTabs from '@/components/ReportTabs'
import WidgetGrid from '@/components/WidgetGrid'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Empty, EmptyDescription, EmptyHeader, EmptyMedia, EmptyTitle } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { useDevReload } from '@/hooks/use-dev-reload'
import { useFreshness } from '@/hooks/use-freshness'
import { ApiError, endpoints, type DashboardDetail, type DashboardsResponse, type Widget } from '@/lib/api'
import { rememberDashboard } from '@/lib/last-dashboard'
import { resolve } from '@/lib/ranges'
import { chooseSelection, type Selection } from '@/lib/selection'
import { selectionParams, viewBody, widgetParams, type Switchers } from '@/lib/view'
import { refreshWidget } from '@/lib/widget-query'

const dashboardQuery = (id: number) => ({
  queryKey: ['dashboard', id],
  queryFn: () => endpoints.dashboard(id),
})

/** `/dashboards/:id`: one dashboard in the app shell, as a report tab or a page of its own (D36). */
export default function Dashboard() {
  const id = Number(useParams().id)
  const list = useQuery({ queryKey: ['dashboards'], queryFn: endpoints.dashboards })
  // Moving to another dashboard keeps the current one on screen until the
  // next one has loaded, so the tabs and header do not flash away.
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
        <DashboardView list={list.data} dashboard={detail.data} />
      ) : (
        <PageLoading />
      )}
    </AppShell>
  )
}

function DashboardView({ list, dashboard }: { list: DashboardsResponse; dashboard: DashboardDetail }) {
  const switchers: Switchers = { project: dashboard.follows_project, range: dashboard.follows_range }
  const projects = useQuery({ queryKey: ['projects'], queryFn: endpoints.projects, enabled: switchers.project })
  const all = projects.data?.projects ?? []
  const active = all.filter((p) => !p.archived).map((p) => p.project_id)
  const archived = all.filter((p) => p.archived).map((p) => p.project_id)

  const [url, setURL] = useSearchParams()
  const navigate = useNavigate()
  const client = useQueryClient()
  const sel = chooseSelection(url, dashboard, active, switchers, archived)
  const range = sel.range
    ? resolve(sel.range, list.timezone, new Date(), sel.from && sel.to ? { from: sel.from, to: sel.to } : undefined)
    : undefined
  const { projectId } = sel
  const [from, to] = [range?.from, range?.to]
  const paramsFor = useCallback(
    (w: Widget) => widgetParams(w, { projectId }, from && to ? { from, to } : undefined),
    [projectId, from, to]
  )

  const waiting = switchers.project && !projects.data
  const noProjects = switchers.project && projects.data !== undefined && active.length === 0
  const showGrid = !waiting && !noProjects && dashboard.widgets.length > 0
  const freshness = useFreshness(dashboard.widgets, paramsFor, showGrid)
  const [refreshing, setRefreshing] = useState(false)

  // A failed save only loses the stored selection: the URL still carries it.
  const save = (id: number, body: ReturnType<typeof viewBody>) =>
    endpoints
      .saveView(id, body)
      .then(() => client.invalidateQueries({ queryKey: ['dashboard', id] }))
      .catch(() => {})

  const change = (next: Selection) => {
    setURL(selectionParams(next))
    void save(dashboard.dashboard_id, viewBody(next, switchers))
  }

  // Moving between report tabs carries the selection and saves it on the
  // tab it lands on, with only the parts that tab has switchers for (D35).
  const openReport = (id: number) => {
    navigate({ pathname: `/dashboards/${id}`, search: selectionParams(sel).toString() })
    client
      .fetchQuery(dashboardQuery(id))
      .then((target) => save(id, viewBody(sel, { project: target.follows_project, range: target.follows_range })))
      .catch(() => {})
  }

  const refreshAll = () => {
    setRefreshing(true)
    void Promise.allSettled(freshness.refreshable.map((w) => refreshWidget(client, w, paramsFor(w)))).finally(() =>
      setRefreshing(false)
    )
  }

  const reports = list.dashboards.filter((d) => d.owner === 'system' && !d.archived_at)
  const isReport = dashboard.owner === 'system'

  return (
    <>
      <TopBar>
        {isReport ? (
          <ReportTabs reports={reports} currentId={dashboard.dashboard_id} onSelect={openReport} />
        ) : (
          <span className="text-sm text-muted-foreground">Yours</span>
        )}
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-1 flex-col gap-4 p-3 sm:p-4 lg:p-6">
        {list.dev && list.errors && list.errors.length > 0 && <DevErrors errors={list.errors} />}
        <DashboardHeader
          title={dashboard.title}
          asOf={showGrid ? freshness.asOf : undefined}
          refreshable={showGrid ? freshness.refreshable.length : 0}
          refreshing={refreshing}
          onRefresh={refreshAll}
        >
          {switchers.project && !noProjects && projects.data && (
            <ProjectSwitcher
              projects={all}
              value={sel.projectId}
              onChange={(projectId) => change({ ...sel, projectId })}
            />
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
          <Notice icon={<LayoutGridIcon />} title="No widgets yet — ask your agent to add some" />
        ) : (
          <WidgetGrid widgets={dashboard.widgets} paramsFor={paramsFor} />
        )}
      </div>
    </>
  )
}

function Notice({ icon, title, children }: { icon: React.ReactNode; title: string; children?: React.ReactNode }) {
  return (
    <Empty className="border md:p-10">
      <EmptyHeader>
        <EmptyMedia variant="icon">{icon}</EmptyMedia>
        <EmptyTitle className="text-base">{title}</EmptyTitle>
        {children && <EmptyDescription>{children}</EmptyDescription>}
      </EmptyHeader>
    </Empty>
  )
}

function NoProjects() {
  return (
    <Notice icon={<FolderPlusIcon />} title="Create a project first">
      This dashboard shows one project at a time, and there are no active projects. Ask your agent to call{' '}
      <code className="font-mono text-foreground">create_project</code>, or run{' '}
      <code className="font-mono text-foreground">twillingate project create -name "My App"</code>.
    </Notice>
  )
}

function DevErrors({ errors }: { errors: { dir: string; message: string }[] }) {
  return (
    <Alert variant="destructive">
      <TriangleAlertIcon />
      <AlertTitle>Some dashboards did not load</AlertTitle>
      <AlertDescription>
        <ul className="list-disc pl-4">
          {errors.map((e) => (
            <li key={e.dir}>
              <span className="font-mono">{e.dir}</span>: {e.message}
            </li>
          ))}
        </ul>
      </AlertDescription>
    </Alert>
  )
}

function PageError({ error, onRetry, bare }: { error: Error; onRetry: () => void; bare?: boolean }) {
  // A 401 is already on its way to the login page.
  if (error instanceof ApiError && error.status === 401) return null
  const notFound = error instanceof ApiError && error.status === 404
  const body = (
    <Notice icon={<TriangleAlertIcon />} title={notFound ? 'No such dashboard' : "Couldn't load this dashboard"}>
      <span className="flex flex-col items-center gap-3">
        {notFound ? 'It may have been deleted.' : error.message}
        {notFound ? (
          <Button asChild variant="outline" size="sm">
            <Link to="/">Go to your dashboards</Link>
          </Button>
        ) : (
          <Button variant="outline" size="sm" onClick={onRetry}>
            Retry
          </Button>
        )}
      </span>
    </Notice>
  )
  if (bare) return body
  return (
    <>
      <TopBar />
      <div className="p-3 sm:p-4 lg:p-6">{body}</div>
    </>
  )
}

function GridSkeleton() {
  return (
    <div className="grid auto-rows-[40px] grid-cols-12 gap-3" aria-busy="true">
      {[0, 1, 2, 3].map((i) => (
        <Skeleton key={i} className="col-span-6 row-span-3 lg:col-span-3" />
      ))}
      <Skeleton className="col-span-12 row-span-8 lg:col-span-6" />
      <Skeleton className="col-span-12 row-span-8 lg:col-span-6" />
    </div>
  )
}

function PageLoading() {
  return (
    <>
      <TopBar>
        <Skeleton className="h-5 w-48" />
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-col gap-4 p-3 sm:p-4 lg:p-6">
        <Skeleton className="h-8 w-40" />
        <GridSkeleton />
      </div>
    </>
  )
}
