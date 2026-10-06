import { useQuery } from '@tanstack/react-query'
import { Navigate, useLocation, useParams } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import Crumbs from '@/components/Crumbs'
import { PageError } from '@/components/PageStates'
import ProjectDashboardTab from '@/components/projects/ProjectDashboardTab'
import ProjectName from '@/components/projects/ProjectName'
import ProjectTabBar from '@/components/projects/ProjectTabBar'
import SetupTab from '@/components/projects/SetupTab'
import { Badge } from '@/components/ui/badge'
import { useProjectActions } from '@/hooks/use-project-actions'
import { useProjectTabActions } from '@/hooks/use-project-tab-actions'
import { SETUP_ID } from '@/lib/project-tabs'
import { dashboardsQuery, projectsQuery, projectTabsQuery } from '@/lib/queries'

/** `/projects/:id`: opens on the Setup tab, keeping the URL's range (project tabs D8). */
export function ProjectIndex() {
  const { search } = useLocation()
  return <Navigate to={`setup${search}`} replace />
}

/**
 * `/projects/:id/setup` and `/projects/:id/dashboards/:dashId`: one
 * project, renamed in place, with its tabs (project tabs D1, D8): Setup,
 * then the dashboards shown with the project pinned.
 */
export default function Project() {
  const params = useParams()
  const param = params.id
  const id = Number(param)
  const valid = Number.isInteger(id) && id > 0
  const dashId = params.dashId === undefined ? SETUP_ID : Number(params.dashId)
  const { data: dash } = useQuery(dashboardsQuery)
  const { data: projectsData, isLoading } = useQuery(projectsQuery)
  const tabsQ = useQuery({ ...projectTabsQuery(id), enabled: valid })
  const actions = useProjectActions()
  const tabActions = useProjectTabActions()
  const project = valid ? projectsData?.projects?.find((p) => p.project_id === id) : undefined

  return (
    <AppShell dashboards={dash?.dashboards ?? []} currentId={0} readOnly={dash?.dev === true}>
      <TopBar>
        <Crumbs items={[{ label: 'Projects', to: '/projects' }, { label: project?.name ?? String(param) }]} />
      </TopBar>
      <div
        className={`mx-auto flex w-full flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6 ${dashId === SETUP_ID ? 'max-w-[1200px]' : 'max-w-[1600px]'}`}
      >
        {!project ? (
          (!valid || !isLoading) && <p className="text-sm text-muted-foreground">No project {param}</p>
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
              />
            </div>
            {dashId === SETUP_ID ? (
              <SetupTab project={project} dash={dash} actions={actions} />
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
