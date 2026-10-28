import { Fragment, useState } from 'react'
import { DndContext, closestCenter } from '@dnd-kit/core'
import { SortableContext, rectSortingStrategy } from '@dnd-kit/sortable'
import { useQuery } from '@tanstack/react-query'
import { PlusIcon } from 'lucide-react'
import AppShell, { TopBar } from '@/components/AppShell'
import Crumbs from '@/components/Crumbs'
import IssuedKeyView from '@/components/projects/IssuedKeyView'
import LoadError from '@/components/projects/LoadError'
import ProjectCard from '@/components/projects/ProjectCard'
import ProjectFormDialog from '@/components/projects/ProjectFormDialog'
import SortableProjectCard from '@/components/projects/SortableProjectCard'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useProjectActions } from '@/hooks/use-project-actions'
import { useReorder } from '@/hooks/use-reorder'
import type { CreatedProject } from '@/lib/api'
import { afterAt } from '@/lib/arrange'
import { dashboardsQuery, keysQuery, limitsQuery, projectActivityQuery, projectsQuery, usageQuery } from '@/lib/queries'
import { formatExact, formatValue } from '@/lib/format'
import { eventsByMonth, formatBytes, formatGrowth, monthStart } from '@/lib/units'

const MONTH = new Intl.DateTimeFormat('en-US', { month: 'short', timeZone: 'UTC' })

/**
 * `/projects`: every live project as a card with its last 30 days, the
 * server's events by month, and New project. Cards drag to a new order, the one every project
 * list shows; archived projects keep their place in it and are listed on
 * the Archive page.
 */
export default function Projects() {
  const { data: dash } = useQuery(dashboardsQuery)
  const projectsQ = useQuery(projectsQuery)
  const statsQ = useQuery(usageQuery({}))
  const keysQ = useQuery(keysQuery())
  // Six months of files to read: a query of its own, so the cards do not wait for it.
  const { data: monthsData } = useQuery(usageQuery({ from: monthStart(5) }))
  const { data: projectsData } = projectsQ
  const { data: statsData } = statsQ
  const { data: keysData } = keysQ
  const { data: limitsData } = useQuery(limitsQuery)
  // Reporting dev serves no forms: the cards ask for no activity there.
  const { data: activityData } = useQuery({ ...projectActivityQuery, enabled: dash !== undefined && !dash.dev })
  const { create, move, pending } = useProjectActions()
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<CreatedProject | null>(null)

  const projects = projectsData?.projects ?? []
  const months = eventsByMonth(monthsData?.projects ?? [])
  const active = projects.filter((p) => !p.archived)
  const activeIds = active.map((p) => p.project_id)
  const readOnly = dash?.dev === true
  const { order, busy, context } = useReorder(
    activeIds,
    (id, to) => move(id, afterAt(activeIds, id, to)),
    'xy',
    (id) => projects.find((p) => p.project_id === id)?.name ?? String(id)
  )
  const byId = new Map(active.map((p) => [p.project_id, p]))
  const statsOf = (id: number) => statsData?.projects.find((s) => s.project_id === id)
  const keysOf = (id: number) => keysData?.keys.filter((k) => k.project_id === id)
  const activityOf = (id: number) => activityData?.projects.find((a) => a.project_id === id)
  // A failed refetch behind data already on screen is not an error line.
  const statsFailed = statsQ.isError && !statsData
  const keysFailed = keysQ.isError && !keysData
  const retryUsage = () => {
    if (statsFailed) void statsQ.refetch()
    if (keysFailed) void keysQ.refetch()
  }

  return (
    <AppShell dashboards={dash?.dashboards ?? []} currentId={0} readOnly={dash?.dev === true}>
      <TopBar>
        <Crumbs items={[{ label: 'Projects' }]} />
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        <header className="flex flex-wrap items-end justify-between gap-3">
          <div className="flex flex-col gap-1">
            <h1 className="text-xl font-semibold tracking-tight">Projects</h1>
            <p className="min-h-5 text-sm text-muted-foreground">
              {[
                projectsData ? `${active.length} ${active.length === 1 ? 'project' : 'projects'}` : null,
                statsData ? `${formatBytes(statsData.database_bytes)} on disk` : null,
                statsData ? formatGrowth(statsData.database_series) : null,
              ]
                .filter(Boolean)
                .join(' · ')}
            </p>
            {months.length > 0 && (
              <p className="text-sm text-muted-foreground">
                Events by month:{' '}
                {months.map(({ month, count }, i) => (
                  <Fragment key={month}>
                    {i > 0 && ' · '}
                    <span className="whitespace-nowrap" title={`${formatExact(count)} events`}>
                      {MONTH.format(new Date(`${month}-01T00:00:00Z`))} {formatValue(count)}
                      {i === months.length - 1 && ' so far'}
                    </span>
                  </Fragment>
                ))}
              </p>
            )}
          </div>
          <Button onClick={() => setCreating(true)}>
            <PlusIcon /> New project
          </Button>
        </header>
        {limitsData?.ingest_disabled && (
          <p role="status" className="rounded-lg border border-destructive/50 px-4 py-3 text-sm text-destructive">
            Ingest is disabled on this server: new events and form submissions are refused.
          </p>
        )}
        {projectsQ.isError && !projectsData && (
          <LoadError what="projects" error={projectsQ.error} onRetry={() => void projectsQ.refetch()} />
        )}
        {(statsFailed || keysFailed) && (
          <LoadError what={statsFailed ? 'usage' : 'keys'} error={(statsFailed ? statsQ.error : keysQ.error)!} onRetry={retryUsage} />
        )}
        {projectsData && active.length === 0 && <p className="text-sm text-muted-foreground">No projects yet. Create one to get an ingest key.</p>}
        <DndContext collisionDetection={closestCenter} {...context}>
          <SortableContext items={order} strategy={rectSortingStrategy}>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {order.map((id) => byId.get(id)).filter((p) => p !== undefined).map((p) => (
                <SortableProjectCard key={p.project_id} id={p.project_id} movable={!readOnly} disabled={busy}>
                  <ProjectCard project={p} stats={statsOf(p.project_id)} keys={keysOf(p.project_id)} statsFailed={statsFailed} activity={activityOf(p.project_id)} />
                </SortableProjectCard>
              ))}
            </div>
          </SortableContext>
        </DndContext>
      </div>
      <ProjectFormDialog
        open={creating}
        onOpenChange={setCreating}
        title="New project"
        submitLabel="Create"
        pending={pending}
        onSubmit={async (body) => {
          const out = await create(body)
          if (out) setCreated(out)
          return out !== undefined
        }}
      />
      <Dialog open={created !== null} onOpenChange={(o) => !o && setCreated(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Project created</DialogTitle>
            <DialogDescription>Its first ingest key and the snippet to install it. The snippet is shown here once; the key stays on the project page.</DialogDescription>
          </DialogHeader>
          {created && <IssuedKeyView keyValue={created.key} snippet={created.snippet} note={created.note} />}
        </DialogContent>
      </Dialog>
    </AppShell>
  )
}
