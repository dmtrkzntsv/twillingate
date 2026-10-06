import { CopyIcon, MoreHorizontalIcon, EyeIcon } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'

interface Props {
  dashboard: { dashboard_id: number; title: string }
  /** The group's menu: duplicate all of it. Otherwise one tab's: copy that tab alone. */
  wholeGroup?: boolean
  /** The group is hidden from the sidebar: its menu offers "Show in sidebar". */
  hidden?: boolean
  /** What the whole-group menu is named for a screen reader: the group's name, as shown. Defaults to the dashboard's title. */
  name?: string
}

/**
 * A Dashboards gallery "…" menu (D17), hidden from the sidebar or not,
 * never archiving anything. A group's (on its card, or on a group of one) offers
 * "Duplicate dashboard", copying the whole system group (`wholeGroup`); a
 * tab's offers "Copy to new dashboard", that tab as a dashboard of its
 * own. Either opens the copy. A group hidden from the sidebar also
 * offers "Show in sidebar", which puts it back: the gallery is where a
 * hidden system dashboard comes back from.
 */
export default function TemplateMenu({ dashboard, wholeGroup = false, hidden = false, name = dashboard.title }: Props) {
  const { duplicate, setSidebar, pending } = useDashboardActions()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="icon"
          className="size-8"
          aria-label={wholeGroup ? `${name} actions` : `${dashboard.title} tab actions`}
        >
          <MoreHorizontalIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {wholeGroup && hidden && (
          <DropdownMenuItem disabled={pending} onClick={() => void setSidebar({ dashboard_id: dashboard.dashboard_id, title: name }, true)}>
            <EyeIcon />
            Show in sidebar
          </DropdownMenuItem>
        )}
        <DropdownMenuItem
          disabled={pending}
          onClick={() => void (wholeGroup ? duplicate(dashboard, { wholeGroup: true }) : duplicate(dashboard))}
        >
          <CopyIcon />
          {wholeGroup ? 'Duplicate dashboard' : 'Copy to new dashboard'}
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
