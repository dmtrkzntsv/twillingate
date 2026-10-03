import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronRightIcon, PlusIcon } from 'lucide-react'
import AppShell, { TopBar } from '@/components/AppShell'
import CopyButton from '@/components/projects/CopyButton'
import LimitsPanel from '@/components/projects/LimitsPanel'
import ProjectCard from '@/components/projects/ProjectCard'
import ProjectFormDialog from '@/components/projects/ProjectFormDialog'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { useProjectActions } from '@/hooks/use-project-actions'
import type { CreatedProject } from '@/lib/api'
import { dashboardsQuery, keysQuery, limitsQuery, projectsQuery, statsQuery } from '@/lib/queries'
import { formatBytes } from '@/lib/units'

/** `/projects`: every project as a card with its last 30 days, the caps, archived projects, and New project. */
export default function Projects() {
  const { data: dash } = useQuery(dashboardsQuery)
  const { data: projectsData } = useQuery(projectsQuery)
  const { data: statsData } = useQuery(statsQuery({}))
  const { data: keysData } = useQuery(keysQuery())
  const { data: limitsData } = useQuery(limitsQuery)
  const { create, restore, pending } = useProjectActions()
  const [creating, setCreating] = useState(false)
  const [created, setCreated] = useState<CreatedProject | null>(null)

  const projects = projectsData?.projects ?? []
  const active = projects.filter((p) => !p.archived)
  const archived = projects.filter((p) => p.archived)
  const statsOf = (id: number) => statsData?.projects.find((s) => s.project_id === id)
  const keysOf = (id: number) => (keysData?.keys ?? []).filter((k) => k.project_id === id)

  return (
    <AppShell dashboards={dash?.dashboards ?? []} currentId={0} readOnly={dash?.dev === true}>
      <TopBar>
        <span className="text-sm text-muted-foreground">Projects</span>
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1600px] flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        <header className="flex flex-wrap items-end justify-between gap-3">
          <div className="flex flex-col gap-1">
            <h1 className="text-xl font-semibold tracking-tight">Projects</h1>
            <p className="text-sm text-muted-foreground">
              {active.length} {active.length === 1 ? 'project' : 'projects'}
              {statsData ? ` · ${formatBytes(statsData.database_bytes)} on disk` : ''}
            </p>
          </div>
          <Button onClick={() => setCreating(true)}>
            <PlusIcon /> New project
          </Button>
        </header>
        {projectsData && active.length === 0 && <p className="text-sm text-muted-foreground">No projects yet. Create one to get an ingest key.</p>}
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
          {active.map((p) => (
            <ProjectCard key={p.project_id} project={p} stats={statsOf(p.project_id)} keys={keysOf(p.project_id)} />
          ))}
        </div>
        {limitsData && <LimitsPanel limits={limitsData.limits} />}
        {archived.length > 0 && (
          <Collapsible className="flex flex-col gap-3">
            <CollapsibleTrigger asChild>
              <Button variant="ghost" className="self-start data-[state=open]:[&>svg]:rotate-90">
                <ChevronRightIcon /> Archived ({archived.length})
              </Button>
            </CollapsibleTrigger>
            <CollapsibleContent className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
              {archived.map((p) => (
                <ProjectCard
                  key={p.project_id}
                  project={p}
                  stats={statsOf(p.project_id)}
                  keys={keysOf(p.project_id)}
                  pending={pending}
                  onRestore={() => void restore(p.project_id)}
                />
              ))}
            </CollapsibleContent>
          </Collapsible>
        )}
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
          {created?.key && (
            <div className="flex items-center gap-2 rounded-md border p-2 font-mono text-sm">
              <span className="flex-1 truncate">{created.key}</span>
              <CopyButton value={created.key} label="Copy key" />
            </div>
          )}
          {created?.snippet && (
            <div className="flex items-start gap-2 rounded-md border p-2">
              <pre className="flex-1 overflow-x-auto font-mono text-xs whitespace-pre-wrap">{created.snippet}</pre>
              <CopyButton value={created.snippet} label="Copy snippet" />
            </div>
          )}
          {created?.note && <p className="text-xs text-muted-foreground">{created.note}</p>}
        </DialogContent>
      </Dialog>
    </AppShell>
  )
}
