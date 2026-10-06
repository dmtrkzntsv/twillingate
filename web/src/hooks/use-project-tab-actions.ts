import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { ApiError, endpoints, type ProjectTab } from '@/lib/api'

export interface ProjectTabActions {
  /** Shows a dashboard as a tab of the project; resolves true when the server took it, false after the toast. */
  add(projectId: number, dashboardId: number, after?: number): Promise<boolean>
  /** Takes a tab off the project's page; the toast "Removed 'X'" has an Undo that adds it back. */
  remove(projectId: number, tab: { dashboard_id: number; title: string }): Promise<boolean>
  /** Reorders one of the user's own tabs after another (0 first among them). */
  move(projectId: number, dashboardId: number, after: number): Promise<boolean>
  /** True while a call runs; buttons wait on it. Per hook call. */
  pending: boolean
}

/**
 * The actions the project page and its "Project tabs…" dialog write a
 * project's tabs with. Each is the existing audited route and answers with
 * the page's tabs after the call, which go straight into the
 * `['project-tabs', id]` cache (any fetch of it in flight cancelled); the
 * dashboards' `project_ids` are refetched. A refusal shows its message in
 * a toast and resolves false, as does a dropped connection, so callers
 * never see a rejection.
 */
export function useProjectTabActions(): ProjectTabActions {
  const queryClient = useQueryClient()
  const [pending, setPending] = useState(false)

  const run = useCallback(
    async (projectId: number, fn: () => Promise<{ tabs: ProjectTab[] }>) => {
      setPending(true)
      try {
        const { tabs } = await fn()
        // A fetch of the tabs begun before the write would answer with the
        // old list after this one and put a removed tab back.
        await queryClient.cancelQueries({ queryKey: ['project-tabs', projectId] })
        queryClient.setQueryData(['project-tabs', projectId], { tabs })
        void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
        return true
      } catch (err) {
        if (err instanceof ApiError) toast.error(err.message)
        else if (err instanceof TypeError) toast.error("Couldn't reach the server")
        else toast.error(err instanceof Error ? err.message : String(err))
        return false
      } finally {
        setPending(false)
      }
    },
    [queryClient]
  )

  const add = useCallback(
    (projectId: number, dashboardId: number, after?: number) =>
      run(projectId, () => endpoints.addProjectTab(projectId, { dashboard_id: dashboardId, after })),
    [run]
  )

  const remove = useCallback(
    async (projectId: number, tab: { dashboard_id: number; title: string }) => {
      const ok = await run(projectId, () => endpoints.removeProjectTab(projectId, tab.dashboard_id))
      if (ok) {
        toast(`Removed '${tab.title}'`, {
          action: { label: 'Undo', onClick: () => void add(projectId, tab.dashboard_id) },
        })
      }
      return ok
    },
    [run, add]
  )

  const move = useCallback(
    (projectId: number, dashboardId: number, after: number) =>
      run(projectId, () => endpoints.moveProjectTab(projectId, dashboardId, after)),
    [run]
  )

  return { add, remove, move, pending }
}
