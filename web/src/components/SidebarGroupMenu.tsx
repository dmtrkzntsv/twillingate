import { ArchiveIcon, ArrowDownIcon, ArrowUpIcon, CopyIcon, EyeOffIcon, MoreHorizontalIcon, PencilIcon } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { SidebarMenuAction, useSidebar } from '@/components/ui/sidebar'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import { groupName, moveGroupBody, type Group } from '@/lib/arrange'

interface Props {
  /** The group this entry is for. */
  group: Group
  /** Every live user group, in sidebar order, for computing Move up/down (D10, D15). */
  userGroups: Group[]
  currentId: number
  /** Opens the entry's name field; user groups only (D9). */
  onRename?: () => void
}

/**
 * The sidebar entry's "…" menu (D10), acting on the whole group, named
 * by its `group_title`, else its first live member's title: Rename first on a user group (its name field,
 * D9), Duplicate on both kinds, Archive on a user group
 * and Hide on a system one, Move up and Move down added for a user
 * group. Duplicate always copies the whole group (`wholeGroup`) and never
 * archives anything — to replace a system group in the sidebar, Duplicate
 * it, then Hide the original. Hide archives the system group whole
 * (a system dashboard refuses to archive alone); it stays off the Archive
 * page and comes back from the Dashboards gallery's "Show in sidebar".
 * `showOnHover` keeps the trigger out of the way until hovered on
 * desktop; the sidebar shows it unconditionally on phones.
 */
export default function SidebarGroupMenu({ group, userGroups, currentId, onRename }: Props) {
  const { duplicate, archive, move, pending } = useDashboardActions()
  const { isMobile, setOpenMobile } = useSidebar()
  const first = group.members[0]
  const name = groupName(group.members)
  const navigateTo = group.members.some((m) => m.dashboard_id === currentId) ? '/dashboards' : undefined
  const index = userGroups.findIndex((g) => g.groupId === group.groupId)

  // Same drawer dismissal as the sidebar's own links (AppSidebar's `close`,
  // D37): a menu action that opens another dashboard must close the phone
  // drawer too, or it stays open over the page it navigated to.
  const close = () => {
    if (isMobile) setOpenMobile(false)
  }

  const moveTo = (to: number) => {
    const body = moveGroupBody(userGroups, group.groupId, to)
    if (body) void move(first.dashboard_id, body)
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <SidebarMenuAction showOnHover aria-label={`${name} actions`}>
          <MoreHorizontalIcon />
        </SidebarMenuAction>
      </DropdownMenuTrigger>
      <DropdownMenuContent side="right" align="start">
        {group.owner === 'user' && onRename && (
          <>
            <DropdownMenuItem disabled={pending} onClick={onRename}>
              <PencilIcon />
              Rename
            </DropdownMenuItem>
            <DropdownMenuSeparator />
          </>
        )}
        <DropdownMenuItem
          disabled={pending}
          onClick={() => {
            close()
            void duplicate(first, { wholeGroup: true })
          }}
        >
          <CopyIcon />
          Duplicate
        </DropdownMenuItem>
        <DropdownMenuItem
          disabled={pending}
          onClick={() => {
            if (navigateTo) close()
            void archive({ dashboard_id: first.dashboard_id, title: name }, { wholeGroup: true, navigateTo, hidden: group.owner === 'system' })
          }}
        >
          {group.owner === 'system' ? <EyeOffIcon /> : <ArchiveIcon />}
          {group.owner === 'system' ? 'Hide' : 'Archive'}
        </DropdownMenuItem>
        {group.owner === 'user' && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled={pending || index <= 0} onClick={() => moveTo(index - 1)}>
              <ArrowUpIcon />
              Move up
            </DropdownMenuItem>
            <DropdownMenuItem disabled={pending || index === userGroups.length - 1} onClick={() => moveTo(index + 1)}>
              <ArrowDownIcon />
              Move down
            </DropdownMenuItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
