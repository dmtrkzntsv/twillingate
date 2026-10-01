import { ArchiveIcon, ArrowDownIcon, ArrowUpIcon, CopyIcon, MoreHorizontalIcon } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { SidebarMenuAction } from '@/components/ui/sidebar'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import { moveGroupBody, type Group } from '@/lib/arrange'

interface Props {
  /** The group this entry is for. */
  group: Group
  /** Every live user group, in sidebar order, for computing Move up/down (D10, D15). */
  userGroups: Group[]
  currentId: number
}

/**
 * The sidebar entry's "…" menu (D10), acting on the whole group named by
 * its first live member: Duplicate and Archive on both kinds, Move up and
 * Move down added for a user group. A system group's Duplicate carries
 * the "replaces it" hint because the server's default `archive_source`
 * copies the group and archives the original (duplicating-a-system-group
 * design); its Archive always passes `wholeGroup`, since a system
 * dashboard refuses to archive alone.
 * `showOnHover` keeps the trigger out of the way until hovered on
 * desktop; the sidebar shows it unconditionally on phones.
 */
export default function SidebarGroupMenu({ group, userGroups, currentId }: Props) {
  const { duplicate, archive, move } = useDashboardActions()
  const first = group.members[0]
  const navigateTo = group.members.some((m) => m.dashboard_id === currentId) ? '/' : undefined
  const index = userGroups.findIndex((g) => g.groupId === group.groupId)

  const moveTo = (to: number) => {
    const body = moveGroupBody(userGroups, group.groupId, to)
    if (body) void move(first.dashboard_id, body)
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <SidebarMenuAction showOnHover aria-label={`${first.title} actions`}>
          <MoreHorizontalIcon />
        </SidebarMenuAction>
      </DropdownMenuTrigger>
      <DropdownMenuContent side="right" align="start">
        {group.owner === 'system' ? (
          <DropdownMenuItem onClick={() => void duplicate(first, {})}>
            <CopyIcon />
            <div className="flex flex-col">
              <span>Duplicate</span>
              <span className="text-xs text-muted-foreground">Your copy replaces it in the sidebar</span>
            </div>
          </DropdownMenuItem>
        ) : (
          <DropdownMenuItem onClick={() => void duplicate(first, { wholeGroup: true })}>
            <CopyIcon />
            Duplicate
          </DropdownMenuItem>
        )}
        <DropdownMenuItem onClick={() => void archive(first, { wholeGroup: true, navigateTo })}>
          <ArchiveIcon />
          Archive
        </DropdownMenuItem>
        {group.owner === 'user' && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled={index <= 0} onClick={() => moveTo(index - 1)}>
              <ArrowUpIcon />
              Move up
            </DropdownMenuItem>
            <DropdownMenuItem disabled={index === userGroups.length - 1} onClick={() => moveTo(index + 1)}>
              <ArrowDownIcon />
              Move down
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
