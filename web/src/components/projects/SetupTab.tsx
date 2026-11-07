import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router'
import DashboardHeader from '@/components/DashboardHeader'
import RangeSwitcher from '@/components/RangeSwitcher'
import AttributesSection from '@/components/projects/AttributesSection'
import DangerSection from '@/components/projects/DangerSection'
import EventsSection from '@/components/projects/EventsSection'
import InstallSection from '@/components/projects/InstallSection'
import KeysSection from '@/components/projects/KeysSection'
import DomainsSection from '@/components/projects/DomainsSection'
import MonitoringSection from '@/components/projects/MonitoringSection'
import OriginsSection from '@/components/projects/OriginsSection'
import UsageSection from '@/components/projects/UsageSection'
import { Button } from '@/components/ui/button'
import type { ProjectActions } from '@/hooks/use-project-actions'
import type { DashboardsResponse, Project } from '@/lib/api'
import { rangeParams, SETTINGS_SECTIONS, settingsPath, type SettingsSection } from '@/lib/project-tabs'
import { keysQuery } from '@/lib/queries'
import { resolve } from '@/lib/ranges'
import { chooseSelection } from '@/lib/selection'
import { selectionParams } from '@/lib/view'

interface Props {
  project: Project
  /** The dashboards list, for the timezone and the purge window. */
  dash?: DashboardsResponse
  actions: ProjectActions
  /** The Settings tab on screen, one the project has (`settingsSections`). */
  section: SettingsSection
}

/** The tabs whose figures cover a range, and so show the range switcher. */
const RANGED: ReadonlySet<SettingsSection> = new Set(['overview', 'events', 'attributes'])

/** The range a tab opens on when the URL names none: a month of usage on Overview, a week elsewhere. */
const defaultRange = (section: SettingsSection) => (section === 'overview' ? '30d' : '7d')

/**
 * A project's Settings page (project landing D4), one tab at a time
 * (project settings tabs): Overview, its usage by month and by day, then
 * how to install the script and the ingest keys; Domains, the allowed
 * origins, then the monitors that check them and when their domains
 * expire; Events; Attributes; and Danger, with Archive. An archived
 * project, which takes nothing and refuses every change, has no install or
 * keys on Overview, no Danger tab and only its origins under Domains, and
 * Restore in the header instead. The range is the URL's, which every tab
 * of the project shares (D8), else 30 days on Overview and 7 elsewhere;
 * only the tabs that count something over it show the switcher.
 */
export default function SetupTab({ project, dash, actions, section }: Props) {
  const id = project.project_id
  const keysQ = useQuery({ ...keysQuery(id), enabled: section === 'overview' && !project.archived })
  const [url, setURL] = useSearchParams()
  const sel = chooseSelection(url, { range: defaultRange(section) }, [], { project: false, range: true })
  const tz = dash?.timezone ?? 'UTC'
  const range = resolve(sel.range!, tz, new Date(), sel.from && sel.to ? { from: sel.from, to: sel.to } : undefined)
  const title = SETTINGS_SECTIONS.find((s) => s.id === section)!.title

  return (
    <>
      {/* The same header as a dashboard tab, so switching tabs moves nothing above the cards. */}
      <DashboardHeader title={title}>
        {RANGED.has(section) && (
          <RangeSwitcher value={{ range: sel.range!, from: sel.from, to: sel.to }} timezone={tz} onChange={(r) => setURL(selectionParams(r))} />
        )}
        {project.archived && (
          <Button variant="outline" size="sm" disabled={actions.pending} onClick={() => void actions.restore(id)}>Restore</Button>
        )}
      </DashboardHeader>
      {section === 'overview' && <UsageSection projectId={id} range={range} />}
      {section === 'overview' && !project.archived && (
        <>
          <InstallSection project={project} keys={keysQ.data?.keys} error={keysQ.error} onRetry={() => void keysQ.refetch()} note={keysQ.data?.note} domainsPath={settingsPath(id, 'domains', rangeParams(url).toString())} />
          <KeysSection
            keys={keysQ.data?.keys}
            error={keysQ.error}
            onRetry={() => void keysQ.refetch()}
            pending={actions.pending}
            onIssue={(label) => actions.issueKey(id, label)}
            onDisable={(label) => actions.disableKey(id, label)}
            onEnable={(label) => actions.enableKey(id, label)}
          />
        </>
      )}
      {section === 'domains' && (
        <>
          <OriginsSection project={project} pending={actions.pending} onSave={(body) => actions.update(id, body)} />
          {/* Monitoring checks the origins above, so it sits right under them. */}
          {!project.archived && (
            <>
              <MonitoringSection projectId={id} pending={actions.pending} onSave={(body) => actions.updateMonitor(id, body)} onCheck={(origin) => actions.checkMonitor(id, origin)} />
              <DomainsSection projectId={id} />
            </>
          )}
        </>
      )}
      {section === 'events' && (
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
      )}
      {section === 'attributes' && (
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
      )}
      {section === 'danger' && !project.archived && (
        <DangerSection name={project.name} purgeDays={dash?.purge_after_days} pending={actions.pending} onArchive={() => void actions.archive(id)} />
      )}
    </>
  )
}
