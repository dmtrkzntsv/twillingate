import { useCallback, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router'
import { toast } from 'sonner'
import { ApiError, endpoints, type MoveBody } from '@/lib/api'

export interface DashboardActions {
  /**
   * Copies the dashboard (or its group with `wholeGroup`) and opens the
   * copy: a dashboard of its own, or with `groupId` a tab of that group.
   * Never archives anything (tabs D10).
   */
  duplicate(d: { dashboard_id: number; title: string }, opts?: { wholeGroup?: boolean; groupId?: number }): Promise<void>
  /**
   * Archives the dashboard (or its group with `wholeGroup`); shows an Undo toast and navigates when asked (D1, D12-D13).
   * With `hidden`, the toast says "Hidden": the page's word for archiving a system group, which comes back from the gallery.
   */
  archive(d: { dashboard_id: number; title: string }, opts: { wholeGroup?: boolean; navigateTo?: string; hidden?: boolean }): Promise<void>
  /** Restores the dashboard, or its whole group (D1, tabs D14). */
  restore(id: number, wholeGroup?: boolean): Promise<void>
  /**
   * Moves a tab or a group by naming the dashboard it goes after (D15).
   * Resolves true when the server took it and false when it did not (after
   * the toast), so a drag can drop its optimistic order at once.
   */
  move(id: number, body: MoveBody): Promise<boolean>
  /** True while an action runs, from its request until the list has refetched; buttons wait on it. Per hook call. */
  pending: boolean
}

/**
 * The actions the page writes dashboards with: duplicate, archive, restore
 * and move, each the existing audited route, with the page's own bearer
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

  /** Refetches both; resolves once the list has (a failed refetch resolves too). */
  const refresh = useCallback(async () => {
    void queryClient.invalidateQueries({ queryKey: ['dashboard'] })
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
        if (err instanceof ApiError) toast.error(err.message)
        else if (err instanceof TypeError) toast.error("Couldn't reach the server")
        else toast.error(err instanceof Error ? err.message : String(err))
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
    (d: { dashboard_id: number; title: string }, opts: { wholeGroup?: boolean; navigateTo?: string; hidden?: boolean }) =>
      run(async () => {
        await endpoints.archive(d.dashboard_id, opts.wholeGroup)
        toast(`${opts.hidden ? 'Hidden' : 'Archived'} '${d.title}'`, {
          action: { label: 'Undo', onClick: () => void restore(d.dashboard_id, opts.wholeGroup) },
        })
        const to = opts.navigateTo
        if (to) return () => navigate(to)
      }),
    [run, restore, navigate]
  )

  const duplicate = useCallback(
    (d: { dashboard_id: number; title: string }, opts: { wholeGroup?: boolean; groupId?: number } = {}) =>
      run(async () => {
        const copy = await endpoints.duplicate(d.dashboard_id, { whole_group: opts.wholeGroup, group_id: opts.groupId })
        return () => navigate(`/dashboards/${copy.dashboard_id}`)
      }),
    [run, navigate]
  )

  const move = useCallback(
    async (id: number, body: MoveBody) => {
      let ok = false
      await run(async () => {
        await endpoints.move(id, body)
        ok = true
      })
      return ok
    },
    [run]
  )

  return { duplicate, archive, restore, move, pending }
}
