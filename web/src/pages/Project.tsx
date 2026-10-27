import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { LayoutGridIcon } from 'lucide-react'
import { Link, useLocation, useNavigate, useParams, useSearchParams } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import Crumbs from '@/components/Crumbs'
import NotFound from '@/components/NotFound'
import { GridSkeleton, Notice, PageError } from '@/components/PageStates'
import ProjectDashboardTab from '@/components/projects/ProjectDashboardTab'
import ProjectName from '@/components/projects/ProjectName'
import ProjectTabBar from '@/components/projects/ProjectTabBar'
import SetupTab from '@/components/projects/SetupTab'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useProjectActions } from '@/hooks/use-project-actions'
import { useProjectTabActions } from '@/hooks/use-project-tab-actions'
import { useStuck } from '@/hooks/use-stuck'
import FormPage from '@/components/forms/FormPage'
import FormsTab from '@/components/forms/FormsTab'
import { readLastTab } from '@/lib/last-tab'
import { FORMS_ID, landingPath, rangeParams, SETTINGS_ID, tabPath } from '@/lib/project-tabs'
import { dashboardsQuery, projectActivityQuery, projectsQuery, projectTabsQuery } from '@/lib/queries'
import { cn } from '@/lib/utils'

/** The top bar's height (h-12), where the tab row pins. */
const TOP_BAR = 48

/** The tab id while `/projects/:id` decides where to land: matches no tab. */
const LANDING = -2

/**
 * `/projects/:id` (with `landing`), `/projects/:id/settings`, `/projects/:id/forms` (with `tab="forms"`, and
 * `/forms/:name` for one form) and `/projects/:id/dashboards/:dashId`: one
 * project, renamed in place, with its dashboards as tabs (project tabs D1,
 * D8) and Forms and Settings as buttons beside them (project landing D4).
 *
 * With `landing`, the page goes to the last tab used here, else the first,
 * else Settings (project landing D3). It draws the page around a skeleton
 * meanwhile, and stays mounted across the redirect, so switching projects
 * does not blank the screen.
 */
export default function Project({ tab, landing = false }: { tab?: 'forms'; landing?: boolean } = {}) {
  const params = useParams()
  const [url] = useSearchParams()
  const { search, key: visit } = useLocation()
  const navigate = useNavigate()
  const client = useQueryClient()
  const param = params.id
  const id = Number(param)
  const valid = Number.isInteger(id) && id > 0
  const dashId = landing ? LANDING : tab === 'forms' ? FORMS_ID : params.dashId === undefined ? SETTINGS_ID : Number(params.dashId)
  const { data: dash, error: dashError } = useQuery(dashboardsQuery)
  const { data: projectsData, isLoading } = useQuery(projectsQuery)
  // Reporting dev serves no project tabs and takes no writes: the page asks
  // for none once the list says it is dev, and shows Settings alone.
  const dev = dash?.dev === true
  const tabsQ = useQuery({ ...projectTabsQuery(id), enabled: valid && dash !== undefined && !dev })
  const activity = useQuery({ ...projectActivityQuery, enabled: valid && dash !== undefined && !dev })
  const mine = activity.data?.projects.find((p) => p.project_id === id)
  const actions = useProjectActions()
  const tabActions = useProjectTabActions()
  const project = valid ? projectsData?.projects?.find((p) => p.project_id === id) : undefined
  // The tab row's backdrop shows only while it is pinned, so at rest the sky wash shows through.
  const [tabBar, setTabBar] = useState<HTMLDivElement | null>(null)
  const stuck = useStuck(tabBar, TOP_BAR)

  // Landing redirects from an effect, so the page stays drawn throughout.
  // Each visit fetches the tabs afresh, even with this page already mounted:
  // a tab removed elsewhere since the cache was filled would otherwise land
  // on the "isn't a tab" notice.
  const listed = dash !== undefined
  useEffect(() => {
    if (!landing) return
    const toSettings = () => navigate(`settings${search}`, { replace: true })
    if (!valid || dev || dashError) return void toSettings()
    if (!listed) return
    let live = true
    const range = rangeParams(new URLSearchParams(search)).toString()
    client.fetchQuery({ ...projectTabsQuery(id), staleTime: 0 }).then(
      (data) => live && navigate(landingPath(id, data.tabs, readLastTab(id), range), { replace: true }),
      () => live && toSettings()
    )
    return () => {
      live = false
    }
  }, [landing, visit, valid, dev, dashError, listed, id, search, client, navigate])

  return (
    <AppShell dashboards={dash?.dashboards ?? []} currentId={0} readOnly={dev}>
      <TopBar>
        <Crumbs items={[{ label: 'Projects', to: '/projects' }, { label: project?.name ?? String(param) }]} />
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        {!project ? (
          (!valid || !isLoading) && (
            <NotFound
              title="No project at this address"
              actions={
                <Button asChild variant="outline">
                  <Link to="/projects">Go to your projects</Link>
                </Button>
              }
            >
              There is no project {param}. It may have been deleted, or the link may be wrong.
            </NotFound>
          )
        ) : (
          <>
            <header className="flex min-w-0 items-center gap-2">
              <ProjectName key={project.name} name={project.name} pending={actions.pending} onRename={(name) => actions.update(id, { name })} />
              {project.archived && <Badge variant="outline">Archived</Badge>}
            </header>
            {/* Pinned under the top bar (h-12) on wide screens, and as wide as
                the page's padding, so the cards scroll out of sight beneath it.
                A child of the whole page rather than of the header block, which
                would let it go with the header. */}
            <div
              ref={setTabBar}
              className={cn(
                '-mx-3 -mt-3 px-3 sm:sticky sm:top-12 sm:z-10 sm:-mx-4 sm:px-4 lg:-mx-6 lg:px-6',
                stuck && 'sm:bg-background/55 sm:backdrop-blur-md'
              )}
            >
              <ProjectTabBar
                projectId={id}
                currentId={dashId}
                tabs={tabsQ.data?.tabs ?? []}
                dashboards={dash?.dashboards ?? []}
                actions={tabActions}
                newSubmissions={mine?.new_submissions ?? 0}
                draftForms={mine?.draft_forms ?? 0}
                readOnly={dev}
              />
            </div>
            {mine && mine.last_event_day === null && dashId !== SETTINGS_ID && dashId !== FORMS_ID && dashId !== LANDING && (
              <div role="status" className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-dashed p-3 text-sm">
                <span>No events received yet</span>
                <Link to={tabPath(id, SETTINGS_ID, rangeParams(url).toString())} className="font-medium underline-offset-2 hover:underline">
                  Set up this project
                </Link>
              </div>
            )}
            {dashId === LANDING ? (
              <GridSkeleton />
            ) : dashId === SETTINGS_ID ? (
              <SetupTab project={project} dash={dash} actions={actions} />
            ) : dev ? (
              <Notice icon={<LayoutGridIcon />} title="No project tabs in reporting dev">
                Reporting dev serves the dashboards alone: open one from the sidebar.
              </Notice>
            ) : dashId === FORMS_ID ? (
              params.name === undefined ? (
                <FormsTab project={project} />
              ) : (
                <FormPage key={params.name} project={project} name={params.name} />
              )
            ) : tabsQ.error ? (
              <PageError error={tabsQ.error} onRetry={() => void tabsQ.refetch()} bare />
            ) : (
              <ProjectDashboardTab project={project} dashId={dashId} tabs={tabsQ.data?.tabs} list={dash} actions={tabActions} />
            )}
          </>
        )}
      </div>
    </AppShell>
  )
}
