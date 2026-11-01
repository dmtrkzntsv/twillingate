import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { endpoints, type MoveBody } from '@/lib/api'
import { toastError } from '@/lib/toast-error'

export interface DashboardActions {
  /**
   * Copies the dashboard (or its group with `wholeGroup`) and opens the
   * copy: a dashboard of its own, or with `groupId` a tab of that group.
   * Never archives anything (tabs D10).
   */
  duplicate(d: { dashboard_id: number; title: string }, opts?: { wholeGroup?: boolean; groupId?: number }): Promise<void>
  /**
   * Archives the user's dashboard (or its group with `wholeGroup`); shows an Undo toast and navigates when asked (D1, D12-D13).
   * A built-in dashboard is never archived: `setSidebar` hides it.
   */
  archive(d: { dashboard_id: number; title: string }, opts: { wholeGroup?: boolean; navigateTo?: string }): Promise<void>
  /** Restores the dashboard, or its whole group (D1, tabs D14). */
  restore(id: number, wholeGroup?: boolean): Promise<void>
  /**
   * Puts the dashboard's whole group in or out of the sidebar, a built-in
   * group's way of being hidden and shown; an Undo toast says "Hidden 'X'"
   * or "Shown in the sidebar", and `navigateTo` follows a hide.
   */
  setSidebar(d: { dashboard_id: number; title: string }, sidebar: boolean, opts?: { navigateTo?: string }): Promise<void>
  /**
   * Moves a tab or a group by naming the dashboard it goes after (D15).
   * Resolves true when the server took it and false when it did not (after
   * the toast), so a drag can drop its optimistic order at once.
   */
  move(id: number, body: MoveBody): Promise<boolean>
  /** Retitles the dashboard (its tab); resolves true when saved, false after the toast. */
  rename(id: number, title: string): Promise<boolean>
  /** Names the dashboard's group; resolves true when saved, false after the toast. */
  renameGroup(id: number, title: string): Promise<boolean>
  /** True while an action runs, from its request until the list has refetched; buttons wait on it. Per hook call. */
  pending: boolean
}

/**
 * The actions the page writes dashboards with: duplicate, archive, restore,
 * the sidebar flag, move and rename, each the existing audited route, with the page's own bearer
 * token (D19). Every action refetches the dashboard list and the
 * dashboard shown so the sidebar and the tabs follow, and navigates only
 * once the list is back: "/dashboards" picks from that list, and before the
 * refetch it still offers the dashboard just archived. A refusal shows
 * its message in a toast instead of throwing to the caller, and so does
 * any other failure — a dropped connection throws a bare `TypeError` from
 * `fetch`, not an `ApiError`, and callers (menus, drag handlers) must not
 * see an unhandled rejection either way (D12).
 */
export function useDashboardActions(): DashboardActions {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [pending, setPending] = useState(false)

  /** Refetches both, and the project tabs that name dashboards; resolves once the list has (a failed refetch resolves too). */
  const refresh = useCallback(async () => {
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
    void queryClient.invalidateQueries({ queryKey: ['project-tabs'] })
    await queryClient.invalidateQueries({ queryKey: ['dashboards'] })
  }, [queryClient])

  /** Runs `fn`, refetches, then calls what `fn` returned (a navigation) if it succeeded. */
  const run = useCallback(
    async (fn: () => Promise<(() => void) | void>) => {
      setPending(true)
      let then: (() => void) | void = undefined
      try {
        then = await fn()
      } catch (err) {
        toastError(err)
      } finally {
        await refresh()
        setPending(false)
      }
      then?.()
    },
    [refresh]
  )

  const restore = useCallback(
    (id: number, wholeGroup = false) =>
      run(async () => {
        await endpoints.restore(id, wholeGroup)
      }),
    [run]
  )

  const archive = useCallback(
    (d: { dashboard_id: number; title: string }, opts: { wholeGroup?: boolean; navigateTo?: string }) =>
      run(async () => {
        await endpoints.archive(d.dashboard_id, opts.wholeGroup)
        toast(`Archived '${d.title}'`, {
          action: { label: 'Undo', onClick: () => void restore(d.dashboard_id, opts.wholeGroup) },
        })
        const to = opts.navigateTo
        if (to) return () => navigate(to)
      }),
    [run, restore, navigate]
  )

  const setSidebar = useCallback(
    (d: { dashboard_id: number; title: string }, sidebar: boolean, opts: { navigateTo?: string } = {}): Promise<void> =>
      run(async () => {
        await endpoints.setSidebar(d.dashboard_id, sidebar)
        toast(sidebar ? 'Shown in the sidebar' : `Hidden '${d.title}'`, {
          action: { label: 'Undo', onClick: () => void setSidebar(d, !sidebar) },
        })
        const to = opts.navigateTo
        if (to) return () => navigate(to)
      }),
    [run, navigate]
  )

  const duplicate = useCallback(
    (d: { dashboard_id: number; title: string }, opts: { wholeGroup?: boolean; groupId?: number } = {}) =>
      run(async () => {
        const copy = await endpoints.duplicate(d.dashboard_id, { whole_group: opts.wholeGroup, group_id: opts.groupId })
        return () => navigate(`/dashboards/${copy.dashboard_id}`)
      }),
    [run, navigate]
  )

  /** Runs a write that answers whether it was saved: true once the server took it, false after the toast. */
  const save = useCallback(
    async (write: () => Promise<unknown>) => {
      let ok = false
      await run(async () => {
        await write()
        ok = true
      })
      return ok
    },
    [run]
  )

  const move = useCallback((id: number, body: MoveBody) => save(() => endpoints.move(id, body)), [save])
  const rename = useCallback((id: number, title: string) => save(() => endpoints.rename(id, title)), [save])
  const renameGroup = useCallback((id: number, title: string) => save(() => endpoints.renameGroup(id, title)), [save])

  return { duplicate, archive, restore, setSidebar, move, rename, renameGroup, pending }
}
