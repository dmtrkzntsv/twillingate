import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router'
import DashboardHeader from '@/components/DashboardHeader'
import RangeSwitcher from '@/components/RangeSwitcher'
import BreakdownsSection from '@/components/projects/BreakdownsSection'
import CapImpactSection from '@/components/projects/CapImpactSection'
import KeysSection from '@/components/projects/KeysSection'
import OriginsSection from '@/components/projects/OriginsSection'
import UsageSection from '@/components/projects/UsageSection'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import type { ProjectActions } from '@/hooks/use-project-actions'
import type { DashboardsResponse, Project } from '@/lib/api'
import { keysQuery } from '@/lib/queries'
import { resolve } from '@/lib/ranges'
import { chooseSelection } from '@/lib/selection'
import { formatPurgeDate } from '@/lib/time'
import { selectionParams } from '@/lib/view'

interface Props {
  project: Project
  /** The dashboards list, for the timezone and the purge window. */
  dash?: DashboardsResponse
  actions: ProjectActions
}

/**
 * A project's Setup tab (project tabs D1): its usage, allowed origins,
 * breakdowns, keys and cap impact, with Archive or Restore. The range is
 * the URL's, which every tab of the project shares (D8), else 30 days.
 */
export default function SetupTab({ project, dash, actions }: Props) {
  const id = project.project_id
  const keysQ = useQuery(keysQuery(id))
  const [url, setURL] = useSearchParams()
  const [archiving, setArchiving] = useState(false)
  const sel = chooseSelection(url, { range: '30d' }, [], { project: false, range: true })
  const tz = dash?.timezone ?? 'UTC'
  const range = resolve(sel.range!, tz, new Date(), sel.from && sel.to ? { from: sel.from, to: sel.to } : undefined)
  const purgeDays = dash?.purge_after_days
  const purgeOn = purgeDays ? formatPurgeDate(new Date(Date.now() + purgeDays * 86_400_000)) : null

  return (
    <>
      {/* The same header as a dashboard tab, so switching tabs moves nothing above the cards. */}
      <DashboardHeader title="Setup">
        <RangeSwitcher value={{ range: sel.range!, from: sel.from, to: sel.to }} timezone={tz} onChange={(r) => setURL(selectionParams(r))} />
        {project.archived ? (
          <Button variant="outline" size="sm" disabled={actions.pending} onClick={() => void actions.restore(id)}>Restore</Button>
        ) : (
          <Button variant="outline" size="sm" disabled={actions.pending} onClick={() => setArchiving(true)}>Archive</Button>
        )}
      </DashboardHeader>
      <UsageSection projectId={id} range={range} />
      <OriginsSection project={project} pending={actions.pending} onSave={(body) => actions.update(id, body)} />
      <BreakdownsSection project={project} range={range} pending={actions.pending} onSave={(attributes) => actions.update(id, { attributes })} />
      <KeysSection
        keys={keysQ.data?.keys}
        error={keysQ.error}
        onRetry={() => void keysQ.refetch()}
        pending={actions.pending}
        onIssue={(label) => actions.issueKey(id, label)}
        onDisable={(label) => actions.disableKey(id, label)}
        onEnable={(label) => actions.enableKey(id, label)}
      />
      <CapImpactSection projectId={id} range={range} />
      <AlertDialog open={archiving} onOpenChange={setArchiving}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Archive {project.name}?</AlertDialogTitle>
            <AlertDialogDescription>
              Ingestion stops; data and dashboards keep working.
              {purgeOn ? ` Unless restored, the project and its data are deleted on ${purgeOn} (${purgeDays} days).` : ' It is kept until restored.'}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void actions.archive(id)}>Archive project</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
