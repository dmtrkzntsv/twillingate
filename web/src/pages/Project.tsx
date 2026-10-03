import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useParams } from 'react-router'
import AppShell, { TopBar } from '@/components/AppShell'
import RangeSwitcher, { type RangeValue } from '@/components/RangeSwitcher'
import CapImpactSection from '@/components/projects/CapImpactSection'
import DetailsSection from '@/components/projects/DetailsSection'
import KeysSection from '@/components/projects/KeysSection'
import UsageSection from '@/components/projects/UsageSection'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useProjectActions } from '@/hooks/use-project-actions'
import { dashboardsQuery, keysQuery, projectsQuery } from '@/lib/queries'
import { resolve } from '@/lib/ranges'

/** `/projects/:id`: one project's usage, details, keys and cap impact, with Archive or Restore. */
export default function Project() {
  const id = Number(useParams().id)
  const { data: dash } = useQuery(dashboardsQuery)
  const { data: projectsData, isLoading } = useQuery(projectsQuery)
  const { data: keysData } = useQuery(keysQuery(id))
  const actions = useProjectActions()
  const [archiving, setArchiving] = useState(false)
  const [rangeValue, setRangeValue] = useState<RangeValue>({ range: '30d' })
  const tz = dash?.timezone ?? 'UTC'
  const range = resolve(rangeValue.range, tz, new Date(), rangeValue.from && rangeValue.to ? { from: rangeValue.from, to: rangeValue.to } : undefined)
  const project = projectsData?.projects.find((p) => p.project_id === id)
  const purgeDays = dash?.purge_after_days

  return (
    <AppShell dashboards={dash?.dashboards ?? []} currentId={0} readOnly={dash?.dev === true}>
      <TopBar>
        <span className="text-sm text-muted-foreground">Projects / {project?.name ?? id}</span>
      </TopBar>
      <div className="mx-auto flex w-full max-w-[1200px] flex-1 flex-col gap-6 p-3 sm:p-4 lg:p-6">
        {!project ? (
          !isLoading && <p className="text-sm text-muted-foreground">No project {id}</p>
        ) : (
          <>
            <header className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex items-center gap-2">
                <h1 className="text-xl font-semibold tracking-tight">{project.name}</h1>
                {project.archived && <Badge variant="outline">Archived</Badge>}
              </div>
              <div className="flex items-center gap-2">
                <RangeSwitcher value={rangeValue} timezone={tz} onChange={setRangeValue} />
                {project.archived ? (
                  <Button variant="outline" disabled={actions.pending} onClick={() => void actions.restore(id)}>Restore</Button>
                ) : (
                  <Button variant="outline" disabled={actions.pending} onClick={() => setArchiving(true)}>Archive</Button>
                )}
              </div>
            </header>
            <UsageSection projectId={id} range={range} />
            <DetailsSection project={project} pending={actions.pending} onSave={(body) => actions.update(id, body)} />
            <KeysSection
              keys={keysData?.keys ?? []}
              pending={actions.pending}
              onIssue={(label) => actions.issueKey(id, label)}
              onDisable={(label) => actions.disableKey(id, label)}
              onEnable={(label) => actions.enableKey(id, label)}
            />
            <CapImpactSection projectId={id} range={range} />
          </>
        )}
      </div>
      <AlertDialog open={archiving} onOpenChange={setArchiving}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Archive {project?.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Ingestion stops; data and dashboards keep working.
              {purgeDays ? ` Unless restored, the project and its data are deleted after ${purgeDays} days.` : ' It is kept until restored.'}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void actions.archive(id)}>Archive project</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </AppShell>
  )
}
