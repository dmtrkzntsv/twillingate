import { ArchiveIcon, ArrowLeftIcon, ArrowRightIcon, CopyIcon, FolderInputIcon, MoreHorizontalIcon } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import type { DashboardDetail, DashboardInfo } from '@/lib/api'
import { liveGroups, moveTabBody, nextAfterArchive } from '@/lib/arrange'

interface Props {
  /** The live user dashboard shown on screen; `dashboard.tabs` is its group, itself included (D11). */
  dashboard: DashboardDetail
  /** Every dashboard, for finding this one's sibling user groups ("Move to"). */
  list: DashboardInfo[]
}

/**
 * The dashboard header's "…" menu (D11): Duplicate, Archive and reordering
 * for a live user dashboard, mirroring the sidebar's group menu but acting
 * on this one tab instead of the whole group. A tab among others gets
 * "Duplicate tab"/"Archive tab" (no `wholeGroup`) plus Move left/right
 * within the group; a lone dashboard's Duplicate and Archive take the
 * whole (one-member) group, and there is nothing to reorder. "Move to"
 * lists every other live user group by its first member's title, with
 * "Own dashboard" (`group_id: 0`) added for a tab that still has company,
 * since a lone dashboard is already its own dashboard.
 */
export default function DashboardMenu({ dashboard, list }: Props) {
  const { duplicate, archive, move, pending } = useDashboardActions()
  const { tabs, dashboard_id: id, group_id: groupId } = dashboard
  const n = tabs.length
  const i = tabs.findIndex((t) => t.dashboard_id === id)
  const otherGroups = liveGroups(list).filter((g) => g.owner === 'user' && g.groupId !== groupId)
  const showMoveTo = n > 1 || otherGroups.length > 0

  const moveLeft = () => {
    const body = moveTabBody(tabs, id, groupId, i - 1)
    if (body) void move(id, body)
  }
  const moveRight = () => {
    const body = moveTabBody(tabs, id, groupId, i + 1)
    if (body) void move(id, body)
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" className="size-8" aria-label="Dashboard actions">
          <MoreHorizontalIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {n > 1 ? (
          <>
            <DropdownMenuItem disabled={pending} onClick={() => void duplicate(dashboard)}>
              <CopyIcon />
              Duplicate tab
            </DropdownMenuItem>
            <DropdownMenuItem disabled={pending} onClick={() => void archive(dashboard, { navigateTo: nextAfterArchive(tabs, id) })}>
              <ArchiveIcon />
              Archive tab
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled={pending || i <= 0} onClick={moveLeft}>
              <ArrowLeftIcon />
              Move left
            </DropdownMenuItem>
            <DropdownMenuItem disabled={pending || i === n - 1} onClick={moveRight}>
              <ArrowRightIcon />
              Move right
            </DropdownMenuItem>
          </>
        ) : (
          <>
            <DropdownMenuItem disabled={pending} onClick={() => void duplicate(dashboard, { wholeGroup: true })}>
              <CopyIcon />
              Duplicate
            </DropdownMenuItem>
            <DropdownMenuItem disabled={pending} onClick={() => void archive(dashboard, { navigateTo: '/' })}>
              <ArchiveIcon />
              Archive
            </DropdownMenuItem>
          </>
        )}
        {showMoveTo && (
          <DropdownMenuSub>
            <DropdownMenuSubTrigger>
              <FolderInputIcon />
              Move to
            </DropdownMenuSubTrigger>
            <DropdownMenuSubContent>
              {otherGroups.map((g) => (
                <DropdownMenuItem key={g.groupId} disabled={pending} onClick={() => void move(id, { group_id: g.groupId })}>
                  {g.members[0].title}
                </DropdownMenuItem>
              ))}
              {n > 1 && (
                <>
                  <DropdownMenuSeparator />
                  <DropdownMenuItem disabled={pending} onClick={() => void move(id, { group_id: 0 })}>
                    Own dashboard
                  </DropdownMenuItem>
                </>
              )}
            </DropdownMenuSubContent>
          </DropdownMenuSub>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
