import {
  ArchiveIcon,
  ArrowLeftIcon,
  ArrowRightIcon,
  CopyIcon,
  CopyPlusIcon,
  FolderInputIcon,
  MoreHorizontalIcon,
  RefreshCwIcon,
  EyeOffIcon,
} from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { useDashboardActions, type DashboardActions } from '@/hooks/use-dashboard-actions'
import type { DashboardDetail, DashboardInfo } from '@/lib/api'
import { groupName, liveGroups, moveTabBody, nextAfterArchive } from '@/lib/arrange'
import { formatInterval } from '@/lib/time'

interface Props {
  /** The dashboard shown on screen; `dashboard.tabs` is its group, itself included (D11). */
  dashboard: DashboardDetail
  /** Every dashboard, for finding this one's sibling user groups ("Move to"). */
  list: DashboardInfo[]
}

/**
 * "Move to": every other live user group by its first member's title,
 * with "Own dashboard" (`group_id: 0`) added for a tab that still has
 * company, since a lone dashboard is already its own dashboard. Nothing
 * when there is nowhere to go.
 */
function MoveTo({ dashboard, list, actions }: Props & { actions: DashboardActions }) {
  const { move, pending } = actions
  const { tabs, dashboard_id: id, group_id: groupId } = dashboard
  const otherGroups = liveGroups(list).filter((g) => g.owner === 'user' && g.groupId !== groupId)
  if (tabs.length < 2 && otherGroups.length === 0) return null
  return (
    <DropdownMenuSub>
      <DropdownMenuSubTrigger>
        <FolderInputIcon />
        Move to
      </DropdownMenuSubTrigger>
      <DropdownMenuSubContent>
        {otherGroups.map((g) => (
          <DropdownMenuItem key={g.groupId} disabled={pending} onClick={() => void move(id, { group_id: g.groupId })}>
            {groupName(g.members)}
          </DropdownMenuItem>
        ))}
        {tabs.length > 1 && (
          <>
            {otherGroups.length > 0 && <DropdownMenuSeparator />}
            <DropdownMenuItem disabled={pending} onClick={() => void move(id, { group_id: 0 })}>
              Own dashboard
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuSubContent>
    </DropdownMenuSub>
  )
}

interface GroupMenuProps {
  dashboard: DashboardDetail
  /** Whether Duplicate and Archive are offered: not in reporting dev, not on an archived user dashboard. */
  editable: boolean
  /** How many cards may be refreshed now (past `refresh_after`), whether a refresh runs, and the refresh. */
  refresh: { count: number; refreshing: boolean; onRefresh: () => void }
  /** The auto-refresh interval and its per-viewer switch; absent when the server allows none. */
  autoRefresh?: { seconds: number; on: boolean; onChange: (on: boolean) => void }
}

/**
 * The top bar's "…" menu, acting on the whole dashboard (its group), like
 * the sidebar's (D10, D11). "Refresh" reloads every card past its
 * `refresh_after`, as the button under the title does; "Auto-refresh
 * every …" is a checkmark that reloads the dashboard on that interval
 * while the window has focus. "Duplicate dashboard" copies every tab and
 * opens the copy, never archiving anything; "Archive dashboard" takes the
 * whole group out of the sidebar and lands on "/dashboards" ("Hide
 * dashboard" on a system group, which comes back from the gallery). An
 * archived group offers neither, since its banner already brings it back.
 */
export function GroupMenu({ dashboard, editable, refresh, autoRefresh }: GroupMenuProps) {
  const { duplicate, archive, pending } = useDashboardActions()
  const first = dashboard.tabs[0] ?? dashboard

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" className="size-8" aria-label="Dashboard actions">
          <MoreHorizontalIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem disabled={refresh.count === 0 || refresh.refreshing} onClick={refresh.onRefresh}>
          <RefreshCwIcon />
          Refresh
        </DropdownMenuItem>
        {autoRefresh && (
          <DropdownMenuCheckboxItem checked={autoRefresh.on} onCheckedChange={(on) => autoRefresh.onChange(on === true)}>
            Auto-refresh every {formatInterval(autoRefresh.seconds)}
          </DropdownMenuCheckboxItem>
        )}
        {editable && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled={pending} onClick={() => void duplicate(first, { wholeGroup: true })}>
              <CopyIcon />
              Duplicate dashboard
            </DropdownMenuItem>
            {!dashboard.archived_at && (
              <DropdownMenuItem
                disabled={pending}
                onClick={() =>
                  void archive(first, { wholeGroup: true, navigateTo: '/dashboards', hidden: dashboard.owner === 'system' })
                }
              >
                {dashboard.owner === 'system' ? <EyeOffIcon /> : <ArchiveIcon />}
                {dashboard.owner === 'system' ? 'Hide dashboard' : 'Archive dashboard'}
              </DropdownMenuItem>
            )}
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

/**
 * The "…" menu beside a tab's title, acting on that tab alone (D11), the
 * same for a group of one tab as of several. Every tab offers "Copy to
 * new dashboard", its copy a dashboard of its own (no `groupId`). A live
 * user tab adds "Duplicate tab", whose copy joins this group right after
 * it, "Archive tab", Move left/right and "Move to". A system tab
 * is only copied out: its group takes no new tabs, and it is never
 * archived or moved on its own.
 */
export function TabMenu({ dashboard, list }: Props) {
  const actions = useDashboardActions()
  const { duplicate, archive, move, pending } = actions
  const { tabs, dashboard_id: id, group_id: groupId } = dashboard
  const n = tabs.length
  const i = tabs.findIndex((t) => t.dashboard_id === id)
  const editable = dashboard.owner === 'user' && !dashboard.archived_at

  const moveTo = (to: number) => {
    const body = moveTabBody(tabs, id, groupId, to)
    if (body) void move(id, body)
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" className="size-7" aria-label="Tab actions">
          <MoreHorizontalIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        {editable && (
          <DropdownMenuItem disabled={pending} onClick={() => void duplicate(dashboard, { groupId })}>
            <CopyIcon />
            Duplicate tab
          </DropdownMenuItem>
        )}
        <DropdownMenuItem disabled={pending} onClick={() => void duplicate(dashboard)}>
          <CopyPlusIcon />
          Copy to new dashboard
        </DropdownMenuItem>
        {editable && (
          <>
            <DropdownMenuItem
              disabled={pending}
              onClick={() => void archive(dashboard, { navigateTo: nextAfterArchive(tabs, id) })}
            >
              <ArchiveIcon />
              Archive tab
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled={pending || i <= 0} onClick={() => moveTo(i - 1)}>
              <ArrowLeftIcon />
              Move left
            </DropdownMenuItem>
            <DropdownMenuItem disabled={pending || i === n - 1} onClick={() => moveTo(i + 1)}>
              <ArrowRightIcon />
              Move right
            </DropdownMenuItem>
            <MoveTo dashboard={dashboard} list={list} actions={actions} />
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
