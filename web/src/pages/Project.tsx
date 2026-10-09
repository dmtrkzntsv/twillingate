import { useQuery } from '@tanstack/react-query'
import { LayoutGridIcon } from 'lucide-react'
import { Link, Navigate, useLocation, useParams } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import Crumbs from '@/components/Crumbs'
import NotFound from '@/components/NotFound'
import { Notice, PageError } from '@/components/PageStates'
import ProjectDashboardTab from '@/components/projects/ProjectDashboardTab'
import ProjectName from '@/components/projects/ProjectName'
import ProjectTabBar from '@/components/projects/ProjectTabBar'
import SetupTab from '@/components/projects/SetupTab'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useProjectActions } from '@/hooks/use-project-actions'
import { useProjectTabActions } from '@/hooks/use-project-tab-actions'
import FormPage from '@/components/forms/FormPage'
import FormsTab from '@/components/forms/FormsTab'
import { readLastTab } from '@/lib/last-tab'
import { FORMS_ID, landingPath, rangeParams, SETTINGS_ID } from '@/lib/project-tabs'
import { dashboardsQuery, projectsQuery, projectTabsQuery } from '@/lib/queries'

/** `/projects/:id`: the last tab used here, else the first, else Settings (project landing D3). */
export function ProjectIndex() {
  const { search } = useLocation()
  const id = Number(useParams().id)
  const valid = Number.isInteger(id) && id > 0
  const { data: dash, error: dashError } = useQuery(dashboardsQuery)
  const dev = dash?.dev === true
  const tabsQ = useQuery({ ...projectTabsQuery(id), enabled: valid && dash !== undefined && !dev })
  const range = rangeParams(new URLSearchParams(search)).toString()
  if (!valid || dev || dashError || tabsQ.error) return <Navigate to={`settings${search}`} replace />
  if (!tabsQ.data) return null
  return <Navigate to={landingPath(id, tabsQ.data.tabs, readLastTab(id), range)} replace />
}

/** `/projects/:id/setup`, the old address of Settings: kept for links and bookmarks. */
export function SetupRedirect() {
  const { search } = useLocation()
  return <Navigate to={`../settings${search}`} relative="path" replace />
}

/**
 * `/projects/:id/settings`, `/projects/:id/forms` (with `tab="forms"`, and
 * `/forms/:name` for one form) and `/projects/:id/dashboards/:dashId`: one
 * project, renamed in place, with its tabs (project tabs D1, D8; forms
 * D12): Settings, Forms, then the dashboards shown with the project pinned.
 */
export default function Project({ tab }: { tab?: 'forms' } = {}) {
  const params = useParams()
  const param = params.id
  const id = Number(param)
  const valid = Number.isInteger(id) && id > 0
  const dashId = tab === 'forms' ? FORMS_ID : params.dashId === undefined ? SETTINGS_ID : Number(params.dashId)
  const { data: dash } = useQuery(dashboardsQuery)
  const { data: projectsData, isLoading } = useQuery(projectsQuery)
  // Reporting dev serves no project tabs and takes no writes: the page asks
  // for none once the list says it is dev, and shows Setup alone.
  const dev = dash?.dev === true
  const tabsQ = useQuery({ ...projectTabsQuery(id), enabled: valid && dash !== undefined && !dev })
  const actions = useProjectActions()
  const tabActions = useProjectTabActions()
  const project = valid ? projectsData?.projects?.find((p) => p.project_id === id) : undefined

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
            <div className="flex min-w-0 flex-col gap-3">
              <header className="flex min-w-0 items-center gap-2">
                <ProjectName key={project.name} name={project.name} pending={actions.pending} onRename={(name) => actions.update(id, { name })} />
                {project.archived && <Badge variant="outline">Archived</Badge>}
              </header>
              <ProjectTabBar
                projectId={id}
                currentId={dashId}
                tabs={tabsQ.data?.tabs ?? []}
                dashboards={dash?.dashboards ?? []}
                actions={tabActions}
                readOnly={dev}
              />
            </div>
            {dashId === SETTINGS_ID ? (
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
