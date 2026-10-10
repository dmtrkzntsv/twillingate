import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { ApiError, endpoints, type ProjectTab } from '@/lib/api'
import { tabPath } from '@/lib/project-tabs'

/** What a failed call tells the person: the server's refusal, or why none came. */
function describe(err: unknown): string {
  if (err instanceof ApiError) return err.message
  if (err instanceof TypeError) return "Couldn't reach the server"
  return err instanceof Error ? err.message : String(err)
}

export interface ProjectTabActions {
  /** Shows a dashboard as a tab of the project; resolves true when the server took it, false after the toast. */
  add(projectId: number, dashboardId: number, after?: number): Promise<boolean>
  /**
   * Makes a dashboard titled so, shows it as the project's last tab and
   * opens it; resolves true when all of it happened, false after the toast.
   * A dashboard made but not added stays in the lists, and the toast says so.
   */
  create(projectId: number, title: string): Promise<boolean>
  /** Takes a tab off the project's page; the toast "Removed 'X'" has an Undo that adds it back. */
  remove(projectId: number, tab: { dashboard_id: number; title: string }): Promise<boolean>
  /** Reorders a tab after another (0 first). */
  move(projectId: number, dashboardId: number, after: number): Promise<boolean>
  /** True while a call runs; buttons wait on it. Per hook call. */
  pending: boolean
}

/**
 * The actions the project page writes its tabs with. Each is the existing
 * audited route and answers with the page's tabs after the call, which go
 * straight into the `['project-tabs', id]` cache (any fetch of it in
 * flight cancelled). A refusal shows its message in a toast and resolves
 * false, as does a dropped connection, so callers never see a rejection.
 */
export function useProjectTabActions(): ProjectTabActions {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
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
        return true
      } catch (err) {
        toast.error(describe(err))
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

  const create = useCallback(
    async (projectId: number, title: string) => {
      let id = 0
      const ok = await run(projectId, async () => {
        id = (await endpoints.createDashboard({ title })).dashboard_id
        try {
          return await endpoints.addProjectTab(projectId, { dashboard_id: id })
        } catch (err) {
          throw new Error(`Created '${title}' but couldn't add it as a tab: ${describe(err)}`)
        }
      })
      // The new dashboard is in the list even when it did not become a tab.
      // Not awaited: each waits for a refetch, and the dialog must not sit open (and submit again) meanwhile.
      if (id) void queryClient.invalidateQueries({ queryKey: ['dashboards'] })
      if (!ok) return false
      void queryClient.invalidateQueries({ queryKey: ['project-tabs', projectId] })
      navigate(tabPath(projectId, id))
      return true
    },
    [run, queryClient, navigate]
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

  return { add, create, remove, move, pending }
}
