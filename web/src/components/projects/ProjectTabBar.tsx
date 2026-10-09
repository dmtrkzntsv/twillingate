import { useState } from 'react'
import { InboxIcon, PlusIcon, SettingsIcon } from 'lucide-react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import ReportTabs from '@/components/ReportTabs'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import type { DashboardInfo, ProjectTab } from '@/lib/api'
import { FORMS_ID, rangeParams, SETTINGS_ID, tabAfter, tabPath } from '@/lib/project-tabs'
import { cn } from '@/lib/utils'
import AddTabDialog from './AddTabDialog'

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
  /** Reporting dev, which takes no writes and serves no forms: no Forms button, no "+" and no dragging. */
  readOnly?: boolean
}

/**
 * A project page's tab row (project landing D1, D4, D5): its dashboards in
 * the project's one order, any of them dragged to a new place, "+" after
 * them; then, outside the scrolling tabs, Forms (with the count of new
 * submissions) and Settings. Switching keeps the range in the URL.
 */
export default function ProjectTabBar({ projectId, currentId, tabs, dashboards, actions, newSubmissions, readOnly = false }: Props) {
  const navigate = useNavigate()
  const [url] = useSearchParams()
  const [adding, setAdding] = useState(false)
  const search = rangeParams(url).toString()
  const open = (id: number) => navigate(tabPath(projectId, id, search))
  const badge = newSubmissions > 99 ? '99+' : String(newSubmissions)
  const formsLabel = newSubmissions > 0 ? `Forms, ${newSubmissions} new` : 'Forms'

  const add = async (id: number) => {
    const ok = await actions.add(projectId, id)
    if (ok) open(id)
    return ok
  }

  return (
    <div className="flex h-10 min-w-0 items-center gap-1 border-b border-border/70">
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
                </Link>
              </Button>
            </TooltipTrigger>
            <TooltipContent>{formsLabel}</TooltipContent>
          </Tooltip>
        )}
        <Tooltip>
          <TooltipTrigger asChild>
            <Button asChild variant="ghost" size="icon" className={cn('size-8', current)} aria-current={currentId === SETTINGS_ID ? 'page' : undefined}>
              <Link to={tabPath(projectId, SETTINGS_ID, search)} aria-label="Settings">
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
      />
    </div>
  )
}
