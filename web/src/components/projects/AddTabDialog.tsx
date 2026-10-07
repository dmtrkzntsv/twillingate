import { useId } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import type { DashboardInfo, ProjectTab } from '@/lib/api'
import { pickerSections } from '@/lib/project-tabs'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Every dashboard, archived ones included; the picker leaves out what can't be added. */
  dashboards: DashboardInfo[]
  /** The project's tabs now: not offered again. */
  tabs: ProjectTab[]
  pending: boolean
  /** Adds the dashboard as a tab; resolves true when the server took it, so the dialog closes. */
  onAdd: (dashboardId: number) => Promise<boolean>
}

/**
 * The "+" picker (project tabs D8): the built-ins not on the project, then
 * the user's live dashboards not on it. It only adds existing dashboards.
 */
export default function AddTabDialog({ open, onOpenChange, dashboards, tabs, pending, onAdd }: Props) {
  const { builtin, own } = pickerSections(dashboards, tabs)
  const add = async (id: number) => {
    if (await onAdd(id)) onOpenChange(false)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add tab</DialogTitle>
          <DialogDescription>Show a dashboard as a tab of this project, with the project pinned.</DialogDescription>
        </DialogHeader>
        {builtin.length === 0 && own.length === 0 ? (
          <p className="text-sm text-muted-foreground">Every dashboard is already a tab of this project</p>
        ) : (
          <div className="flex max-h-[60vh] min-w-0 flex-col gap-4 overflow-y-auto">
            <Section title="Built-in" dashboards={builtin} pending={pending} onPick={add} />
            <Section title="Your dashboards" dashboards={own} pending={pending} onPick={add} />
          </div>
        )}
      </DialogContent>
    </Dialog>
  )
}

function Section({ title, dashboards, pending, onPick }: { title: string; dashboards: DashboardInfo[]; pending: boolean; onPick: (id: number) => void }) {
  const id = useId()
  if (dashboards.length === 0) return null
  return (
    <div role="group" aria-labelledby={id} className="flex min-w-0 flex-col gap-1">
      <h3 id={id} className="text-xs font-medium text-muted-foreground">{title}</h3>
      {dashboards.map((d) => (
        <Button
          key={d.dashboard_id}
          variant="ghost"
          className="min-w-0 justify-start"
          disabled={pending}
          onClick={() => onPick(d.dashboard_id)}
        >
          <span className="truncate">{d.title}</span>
        </Button>
      ))}
    </div>
  )
}
