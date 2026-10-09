import { useState } from 'react'
import { PlusIcon } from 'lucide-react'
import { useNavigate, useSearchParams } from 'react-router'
import ReportTabs from '@/components/ReportTabs'
import { Button } from '@/components/ui/button'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import type { DashboardInfo, ProjectTab } from '@/lib/api'
import { FORMS_ID, rangeParams, SETTINGS_ID, tabAfter, tabPath } from '@/lib/project-tabs'
import AddTabDialog from './AddTabDialog'

interface Props {
  projectId: number
  /** The tab on screen: `SETTINGS_ID`, `FORMS_ID` or a dashboard id. */
  currentId: number
  /** The project's tabs, in order. */
  tabs: ProjectTab[]
  /** Every dashboard, for the "+" picker. */
  dashboards: DashboardInfo[]
  actions: ProjectTabActions
  /** Reporting dev, which takes no writes and serves no forms: no Forms tab, no "+" and no dragging. */
  readOnly?: boolean
}

/**
 * A project page's tab row (project tabs D1, D8; forms D12): Setup, Forms
 * and the built-ins fixed in front, the user's own tabs after them, dragged to a new order,
 * and "+" last. Switching tabs keeps the range in the URL.
 */
export default function ProjectTabBar({ projectId, currentId, tabs, dashboards, actions, readOnly = false }: Props) {
  const navigate = useNavigate()
  const [url] = useSearchParams()
  const [adding, setAdding] = useState(false)
  const search = rangeParams(url).toString()
  const open = (id: number) => navigate(tabPath(projectId, id, search))
  const fixed = readOnly ? [{ dashboard_id: SETTINGS_ID, title: 'Settings' }] : [{ dashboard_id: SETTINGS_ID, title: 'Settings' }, { dashboard_id: FORMS_ID, title: 'Forms' }]
  const fixedIds = [...fixed.map((t) => t.dashboard_id), ...tabs.filter((t) => t.owner === 'system').map((t) => t.dashboard_id)]

  // The row still fixes the built-ins, so `to` counts the user's own tabs: find the tab it lands on.
  const move = (id: number, to: number) => {
    const own = tabs.filter((t) => t.owner === 'user')
    const target = own[Math.min(to, own.length - 1)]
    return actions.move(projectId, id, tabAfter(tabs, id, tabs.findIndex((t) => t.dashboard_id === target.dashboard_id)))
  }

  const add = async (id: number) => {
    const ok = await actions.add(projectId, id)
    if (ok) open(id)
    return ok
  }

  return (
    <div className="flex h-10 min-w-0 items-center gap-1 border-b border-border/70">
      <ReportTabs
        tabs={[...fixed, ...tabs]}
        currentId={currentId}
        onSelect={open}
        fixedIds={fixedIds}
        sortable={!readOnly}
        onMove={readOnly ? undefined : move}
        trailing={
          !readOnly && (
            <Button variant="ghost" size="icon" className="size-8 shrink-0" aria-label="Add tab" onClick={() => setAdding(true)}>
              <PlusIcon />
            </Button>
          )
        }
      />
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
