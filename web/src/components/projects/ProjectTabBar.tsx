import { useState } from 'react'
import { InboxIcon, PlusIcon, SettingsIcon } from 'lucide-react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import ReportTabs from '@/components/ReportTabs'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import type { DashboardInfo, ProjectTab } from '@/lib/api'
import { plural } from '@/lib/format'
import { readLastTab, writeLastTab } from '@/lib/last-tab'
import { FORMS_ID, landingPath, rangeParams, SETTINGS_ID, settingsPath, tabAfter, tabPath, type SettingsSection } from '@/lib/project-tabs'
import { cn } from '@/lib/utils'
import AddTabDialog from './AddTabDialog'
import SettingsTabs from './SettingsTabs'

/** The button for the page on screen reads as an active control. */
const current = 'aria-[current=page]:bg-accent aria-[current=page]:text-foreground'

interface Props {
  projectId: number
  /** The tab on screen: `SETTINGS_ID`, `FORMS_ID` or a dashboard id. */
  currentId: number
  /** The project's tabs, in order. */
  tabs: ProjectTab[]
  /** Every dashboard, for the "+" picker. */
  dashboards: DashboardInfo[]
  actions: ProjectTabActions
  /** Submissions no one has opened yet, across the project's forms; the Forms button's badge. */
  newSubmissions: number
  /** Live draft forms, waiting to be approved: a dot on the Forms button while no submission is new. */
  draftForms: number
  /** Whether the project is archived, which has fewer Settings tabs. */
  archived?: boolean
  /** On Settings (`currentId` is `SETTINGS_ID`): the Settings tab on screen. */
  section?: SettingsSection
  /** While the project page is still landing, where a tab write would cancel its fetch: no Forms button, no "+" and no dragging. */
  readOnly?: boolean
}

/**
 * A project page's tab row (project landing D1, D4, D5): its dashboards in
 * the project's one order, any of them dragged to a new place, "+" after
 * them; then, outside the scrolling tabs, Forms (with the count of new
 * submissions) and Settings. Switching keeps the range in the URL.
 *
 * On Settings the row holds the Settings tabs instead (project settings
 * tabs), and on Forms none: the page's title goes back to the dashboards
 * there (ProjectBackHeader), as the highlighted gear does on Settings.
 */
export default function ProjectTabBar({ projectId, currentId, tabs, dashboards, actions, newSubmissions, draftForms, archived = false, section, readOnly = false }: Props) {
  const navigate = useNavigate()
  const [url] = useSearchParams()
  const [adding, setAdding] = useState(false)
  const search = rangeParams(url).toString()
  const open = (id: number) => {
    // Remembered on the choice, not only once the tab has loaded, so the
    // way back from Settings, from a tab left while loading, goes to it.
    if (tabs.some((t) => t.dashboard_id === id)) writeLastTab(projectId, id)
    navigate(tabPath(projectId, id, search))
  }
  const badge = newSubmissions > 99 ? '99+' : String(newSubmissions)
  const formsLabel = [
    'Forms',
    newSubmissions > 0 && `${newSubmissions} new`,
    draftForms > 0 && plural(draftForms, 'draft'),
  ]
    .filter(Boolean)
    .join(', ')

  const onSettings = currentId === SETTINGS_ID
  // Back from Settings, as opening the project lands (D3); with no dashboard
  // the gear stays where it is.
  const dashboardsPath = tabs.length > 0 ? landingPath(projectId, tabs, readLastTab(projectId), search) : null
  const gearPath = !onSettings ? tabPath(projectId, SETTINGS_ID, search) : (dashboardsPath ?? settingsPath(projectId, section, search))

  const add = async (id: number) => {
    const ok = await actions.add(projectId, id)
    if (ok) open(id)
    return ok
  }

  return (
    <div className="flex h-10 min-w-0 items-center gap-1 border-b border-border/70">
      {onSettings ? (
        <SettingsTabs projectId={projectId} archived={archived} current={section ?? 'overview'} search={search} />
      ) : (
        // The Forms pages show no dashboard tabs: their title goes back to them (ProjectBackHeader).
        currentId !== FORMS_ID && (
          <ReportTabs
            tabs={tabs}
            currentId={currentId}
            onSelect={open}
            sortable={!readOnly}
            onMove={readOnly ? undefined : (id, to) => actions.move(projectId, id, tabAfter(tabs, id, to))}
            trailing={
              !readOnly && (
                <Button variant="ghost" size="icon" className="size-8 shrink-0" aria-label="Add tab" onClick={() => setAdding(true)}>
                  <PlusIcon />
                </Button>
              )
            }
          />
        )
      )}
      <div className="ml-auto flex shrink-0 items-center gap-0.5">
        {!readOnly && (
          <Tooltip>
            <TooltipTrigger asChild>
              <Button asChild variant="ghost" size="icon" className={cn('relative size-8', current)} aria-current={currentId === FORMS_ID ? 'page' : undefined}>
                <Link to={tabPath(projectId, FORMS_ID, search)} aria-label={formsLabel}>
                  <InboxIcon />
                  {newSubmissions > 0 && (
                    <span aria-hidden className="absolute -top-0.5 -right-0.5 min-w-4 rounded-full bg-primary px-1 text-center text-[10px] leading-4 font-medium text-primary-foreground">
                      {badge}
                    </span>
                  )}
                  {newSubmissions === 0 && draftForms > 0 && (
                    <span aria-hidden data-forms-dot className="absolute top-1 right-1 size-2 rounded-full bg-primary" />
                  )}
                </Link>
              </Button>
            </TooltipTrigger>
            <TooltipContent>{formsLabel}</TooltipContent>
          </Tooltip>
        )}
        <Tooltip>
          <TooltipTrigger asChild>
            <Button asChild variant="ghost" size="icon" className={cn('size-8', current)} aria-current={currentId === SETTINGS_ID ? 'page' : undefined}>
              <Link to={gearPath} aria-label="Settings">
                <SettingsIcon />
              </Link>
            </Button>
          </TooltipTrigger>
          <TooltipContent>Settings</TooltipContent>
        </Tooltip>
      </div>
      <AddTabDialog
        open={adding}
        onOpenChange={setAdding}
        dashboards={dashboards}
        tabs={tabs}
        pending={actions.pending}
        onAdd={add}
        onCreate={(title) => actions.create(projectId, title, search)}
      />
    </div>
  )
}
