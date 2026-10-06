import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { Checkbox } from '@/components/ui/checkbox'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Switch } from '@/components/ui/switch'
import { useProjectTabActions } from '@/hooks/use-project-tab-actions'
import { ApiError, endpoints, type DashboardDetail } from '@/lib/api'
import { projectsQuery } from '@/lib/queries'

interface Props {
  dashboard: DashboardDetail
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * "Project tabs…" on a dashboard of the user's own: one checkbox per
 * project (active ones first, then archived) saying whether the dashboard
 * is a tab of it, and "Add to new projects", the dashboard's `project_tab`
 * flag. The boxes and the switch show what the server has: a refusal (the
 * last project tab of a dashboard out of the sidebar is "unreachable")
 * toasts, and the control goes back once the dashboard is refetched.
 */
export default function ProjectTabsDialog({ dashboard, open, onOpenChange }: Props) {
  const { data } = useQuery({ ...projectsQuery, enabled: open })
  const actions = useProjectTabActions()
  const queryClient = useQueryClient()
  const [savingFlag, setSavingFlag] = useState(false)
  const projects = data?.projects ?? []
  const ordered = [...projects.filter((p) => !p.archived), ...projects.filter((p) => p.archived)]
  const on = new Set(dashboard.project_ids)

  const toggle = (projectId: number, checked: boolean) =>
    void (checked ? actions.add(projectId, dashboard.dashboard_id) : actions.remove(projectId, dashboard))

  const setFlag = async (checked: boolean) => {
    setSavingFlag(true)
    try {
      await endpoints.setProjectTab(dashboard.dashboard_id, checked)
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['dashboard'] }),
        queryClient.invalidateQueries({ queryKey: ['dashboards'] }),
      ])
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Couldn't reach the server")
    } finally {
      setSavingFlag(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Project tabs</DialogTitle>
          <DialogDescription>Show this dashboard as a tab of these projects, with the project pinned.</DialogDescription>
        </DialogHeader>
        <div className="grid max-h-[50vh] min-w-0 grid-cols-1 gap-2 overflow-y-auto">
          {ordered.map((p) => (
            <label key={p.project_id} className="flex min-w-0 items-center gap-2 text-sm">
              <Checkbox
                checked={on.has(p.project_id)}
                disabled={actions.pending}
                onCheckedChange={(checked) => toggle(p.project_id, checked === true)}
              />
              <span className="truncate">
                {p.name}
                {p.archived ? ' (archived)' : ''}
              </span>
            </label>
          ))}
        </div>
        <label className="flex min-w-0 items-center gap-2 border-t pt-4 text-sm">
          <Switch checked={dashboard.project_tab} disabled={savingFlag} onCheckedChange={(checked) => void setFlag(checked)} />
          <span>Add to new projects</span>
        </label>
      </DialogContent>
    </Dialog>
  )
}
