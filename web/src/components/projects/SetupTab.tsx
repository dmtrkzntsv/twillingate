import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router'
import DashboardHeader from '@/components/DashboardHeader'
import RangeSwitcher from '@/components/RangeSwitcher'
import AttributesSection from '@/components/projects/AttributesSection'
import DangerSection from '@/components/projects/DangerSection'
import EventsSection from '@/components/projects/EventsSection'
import InstallSection from '@/components/projects/InstallSection'
import KeysSection from '@/components/projects/KeysSection'
import OriginsSection from '@/components/projects/OriginsSection'
import UsageSection from '@/components/projects/UsageSection'
import { Button } from '@/components/ui/button'
import type { ProjectActions } from '@/hooks/use-project-actions'
import type { DashboardsResponse, Project } from '@/lib/api'
import { keysQuery } from '@/lib/queries'
import { resolve } from '@/lib/ranges'
import { chooseSelection } from '@/lib/selection'
import { selectionParams } from '@/lib/view'

interface Props {
  project: Project
  /** The dashboards list, for the timezone and the purge window. */
  dash?: DashboardsResponse
  actions: ProjectActions
}

/**
 * A project's Settings page (project landing D4): its usage, how to install
 * the script (not on an archived project, which takes nothing, or in reporting
 * dev, which has no keys), allowed origins, events, attributes and keys, then
 * Archive in a Danger section; an archived project has Restore in the header
 * instead. The range is the URL's, which every tab of the project shares (D8),
 * else 7 days.
 */
export default function SetupTab({ project, dash, actions }: Props) {
  const id = project.project_id
  const keysQ = useQuery(keysQuery(id))
  const [url, setURL] = useSearchParams()
  const sel = chooseSelection(url, { range: '7d' }, [], { project: false, range: true })
  const tz = dash?.timezone ?? 'UTC'
  const range = resolve(sel.range!, tz, new Date(), sel.from && sel.to ? { from: sel.from, to: sel.to } : undefined)

  return (
    <>
      {/* The same header as a dashboard tab, so switching tabs moves nothing above the cards. */}
      <DashboardHeader title="Settings">
        <RangeSwitcher value={{ range: sel.range!, from: sel.from, to: sel.to }} timezone={tz} onChange={(r) => setURL(selectionParams(r))} />
        {project.archived && (
          <Button variant="outline" size="sm" disabled={actions.pending} onClick={() => void actions.restore(id)}>Restore</Button>
        )}
      </DashboardHeader>
      <UsageSection projectId={id} range={range} />
      {!project.archived && dash && !dash.dev && (
        <InstallSection project={project} keys={keysQ.data?.keys} error={keysQ.error} onRetry={() => void keysQ.refetch()} note={keysQ.data?.note} />
      )}
      <OriginsSection project={project} pending={actions.pending} onSave={(body) => actions.update(id, body)} />
      <EventsSection
        project={project}
        range={range}
        pending={actions.pending}
        onRename={(body) => actions.renameEvent(id, body)}
        onMerge={(body) => actions.mergeEvent(id, body)}
        onBlock={(body) => actions.blockEvent(id, body)}
        onRemove={(body) => actions.removeEventRule(id, body)}
        onAddAlias={(body) => actions.addEventAlias(id, body)}
      />
      <AttributesSection
        project={project}
        range={range}
        pending={actions.pending}
        onRename={(body) => actions.renameAttribute(id, body)}
        onMerge={(body) => actions.mergeAttribute(id, body)}
        onBlock={(body) => actions.blockAttribute(id, body)}
        onRemove={(body) => actions.removeAttributeRule(id, body)}
        onAddAlias={(body) => actions.addAttributeAlias(id, body)}
      />
      <KeysSection
        keys={keysQ.data?.keys}
        error={keysQ.error}
        onRetry={() => void keysQ.refetch()}
        pending={actions.pending}
        onIssue={(label) => actions.issueKey(id, label)}
        onDisable={(label) => actions.disableKey(id, label)}
        onEnable={(label) => actions.enableKey(id, label)}
      />
      {!project.archived && (
        <DangerSection name={project.name} purgeDays={dash?.purge_after_days} pending={actions.pending} onArchive={() => void actions.archive(id)} />
      )}
    </>
  )
}
