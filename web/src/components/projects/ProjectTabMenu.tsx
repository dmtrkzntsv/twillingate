import { useState } from 'react'
import { ArrowLeftIcon, ArrowRightIcon, ExternalLinkIcon, MoreHorizontalIcon, PencilIcon, XIcon } from 'lucide-react'
import { Link, useNavigate } from 'react-router'
import { RenameTabDialog } from '@/components/DashboardMenu'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'
import { useIsMobile } from '@/hooks/use-mobile'
import type { ProjectTabActions } from '@/hooks/use-project-tab-actions'
import type { ProjectTab } from '@/lib/api'
import { SETTINGS_ID, tabAfter, tabPath } from '@/lib/project-tabs'
import { formatInterval } from '@/lib/time'

interface Props {
  projectId: number
  /** The tab on screen. */
  tab: ProjectTab
  /** The project's tabs, in order. */
  tabs: ProjectTab[]
  actions: ProjectTabActions
  /** The range part of the URL, carried to the tab shown after a removal. */
  search: string
  /** The auto-refresh interval and its per-viewer switch, as on the dashboard page; absent when the server allows none. */
  autoRefresh?: { seconds: number; on: boolean; onChange: (on: boolean) => void }
  /** Offers Rename tab: the user's own dashboard, outside reporting dev. */
  renamable?: boolean
}

/**
 * The "…" beside a project tab's title (project tabs D8): Rename tab on
 * one of the user's own dashboards (its title everywhere), Remove from this
 * project (landing on the tab before it, else the one after, else
 * Settings), Open as dashboard with the project chosen, and on phones Move
 * left/right for any tab, which the row drags on wide screens.
 */
export default function ProjectTabMenu({ projectId, tab, tabs, actions, search, autoRefresh, renamable = false }: Props) {
  const navigate = useNavigate()
  const { rename, pending } = useDashboardActions()
  const [renaming, setRenaming] = useState(false)
  const mobile = useIsMobile()
  const id = tab.dashboard_id
  const i = tabs.findIndex((t) => t.dashboard_id === id)
  const next = tabs[i - 1] ?? tabs[i + 1]

  const remove = async () => {
    if (await actions.remove(projectId, tab)) navigate(tabPath(projectId, next?.dashboard_id ?? SETTINGS_ID, search))
  }
  const moveTo = (to: number) => void actions.move(projectId, id, tabAfter(tabs, id, to))

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" className="size-7" aria-label="Tab actions">
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start">
          {renamable && (
            <DropdownMenuItem disabled={pending} onClick={() => setRenaming(true)}>
              <PencilIcon />
              Rename tab…
            </DropdownMenuItem>
          )}
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
          {mobile && (
            <>
              <DropdownMenuSeparator />
              <DropdownMenuItem disabled={actions.pending || i === 0} onClick={() => moveTo(i - 1)}>
                <ArrowLeftIcon />
                Move left
              </DropdownMenuItem>
              <DropdownMenuItem disabled={actions.pending || i === tabs.length - 1} onClick={() => moveTo(i + 1)}>
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
      {renamable && (
        <RenameTabDialog open={renaming} onOpenChange={setRenaming} dashboard={tab} rename={rename} pending={pending} />
      )}
    </>
  )
}
