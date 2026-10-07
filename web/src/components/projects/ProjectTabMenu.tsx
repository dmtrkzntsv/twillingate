import { ArrowLeftIcon, ArrowRightIcon, ExternalLinkIcon, MoreHorizontalIcon, XIcon } from 'lucide-react'
import { Link, useNavigate } from 'react-router'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useIsMobile } from '@/hooks/use-mobile'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import type { ProjectTab } from '@/lib/api'
import { SETUP_ID, tabPath, userAfter } from '@/lib/project-tabs'
import { formatInterval } from '@/lib/time'

interface Props {
  projectId: number
  /** The tab on screen. */
  tab: ProjectTab
  /** The project's tabs after Setup, in order. */
  tabs: ProjectTab[]
  actions: ProjectTabActions
  /** The range part of the URL, carried to the tab shown after a removal. */
  search: string
  /** The auto-refresh interval and its per-viewer switch, as on the dashboard page; absent when the server allows none. */
  autoRefresh?: { seconds: number; on: boolean; onChange: (on: boolean) => void }
}

/**
 * The "…" beside a project tab's title (project tabs D8): Remove from this
 * project (landing on the tab before it, else Setup), Open as dashboard
 * with the project chosen, and on phones Move left/right for the user's
 * own tabs, which the row drags on wide screens.
 */
export default function ProjectTabMenu({ projectId, tab, tabs, actions, search, autoRefresh }: Props) {
  const navigate = useNavigate()
  const mobile = useIsMobile()
  const id = tab.dashboard_id
  const before = tabs[tabs.findIndex((t) => t.dashboard_id === id) - 1]
  const own = tabs.filter((t) => t.owner === 'user')
  const i = own.findIndex((t) => t.dashboard_id === id)

  const remove = async () => {
    if (await actions.remove(projectId, tab)) navigate(tabPath(projectId, before?.dashboard_id ?? SETUP_ID, search))
  }
  const moveTo = (to: number) => void actions.move(projectId, id, userAfter(tabs, id, to))

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" className="size-7" aria-label="Tab actions">
          <MoreHorizontalIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start">
        <DropdownMenuItem disabled={actions.pending} onClick={() => void remove()}>
          <XIcon />
          Remove from this project
        </DropdownMenuItem>
        <DropdownMenuItem asChild>
          <Link to={`/dashboards/${id}?project=${projectId}`}>
            <ExternalLinkIcon />
            Open as dashboard
          </Link>
        </DropdownMenuItem>
        {mobile && i !== -1 && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled={actions.pending || i === 0} onClick={() => moveTo(i - 1)}>
              <ArrowLeftIcon />
              Move left
            </DropdownMenuItem>
            <DropdownMenuItem disabled={actions.pending || i === own.length - 1} onClick={() => moveTo(i + 1)}>
              <ArrowRightIcon />
              Move right
            </DropdownMenuItem>
          </>
        )}
        {autoRefresh && (
          <>
            <DropdownMenuSeparator />
            <DropdownMenuCheckboxItem checked={autoRefresh.on} onCheckedChange={(on) => autoRefresh.onChange(on === true)}>
              Auto-refresh every {formatInterval(autoRefresh.seconds)}
            </DropdownMenuCheckboxItem>
          </>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
