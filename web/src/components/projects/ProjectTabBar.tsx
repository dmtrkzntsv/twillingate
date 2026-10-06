import { useState } from 'react'
import { PlusIcon } from 'lucide-react'
import { useNavigate, useSearchParams } from 'react-router'
import ReportTabs from '@/components/ReportTabs'
import { Button } from '@/components/ui/button'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import type { DashboardInfo, ProjectTab } from '@/lib/api'
import { rangeParams, SETUP_ID, tabPath, userAfter } from '@/lib/project-tabs'
import AddTabDialog from './AddTabDialog'

interface Props {
  projectId: number
  /** The tab on screen: `SETUP_ID` or a dashboard id. */
  currentId: number
  /** The project's tabs after Setup, in order: built-ins, then the user's own. */
  tabs: ProjectTab[]
  /** Every dashboard, for the "+" picker. */
  dashboards: DashboardInfo[]
  actions: ProjectTabActions
  /** Reporting dev, which takes no writes: no "+" and no dragging. */
  readOnly?: boolean
}

/**
 * A project page's tab row (project tabs D1, D8): Setup and the built-ins
 * fixed in front, the user's own tabs after them, dragged to a new order,
 * and "+" last. Switching tabs keeps the range in the URL.
 */
export default function ProjectTabBar({ projectId, currentId, tabs, dashboards, actions, readOnly = false }: Props) {
  const navigate = useNavigate()
  const [url] = useSearchParams()
  const [adding, setAdding] = useState(false)
  const search = rangeParams(url).toString()
  const open = (id: number) => navigate(tabPath(projectId, id, search))
  const fixedIds = [SETUP_ID, ...tabs.filter((t) => t.owner === 'system').map((t) => t.dashboard_id)]

  const add = async (id: number) => {
    const ok = await actions.add(projectId, id)
    if (ok) open(id)
    return ok
  }

  return (
    <div className="flex h-10 min-w-0 items-center gap-1 border-b border-border/70">
      <ReportTabs
        tabs={[{ dashboard_id: SETUP_ID, title: 'Setup' }, ...tabs]}
        currentId={currentId}
        onSelect={open}
        fixedIds={fixedIds}
        sortable={!readOnly}
        onMove={readOnly ? undefined : (id, to) => actions.move(projectId, id, userAfter(tabs, id, to))}
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
