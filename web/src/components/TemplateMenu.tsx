import { CopyIcon, MoreHorizontalIcon } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Button } from '@/components/ui/button'
import { useDashboardActions } from '@/hooks/use-dashboard-actions'

interface Props {
  /** The template group's first member. */
  first: { dashboard_id: number; title: string }
}

/**
 * A Dashboards gallery row's "…" menu (D17): "Duplicate dashboard" copies the whole system
 * group (`wholeGroup`) and opens the copy, archived or not, never
 * archiving anything. A single tab is duplicated from its own "…" menu
 * once the template is open.
 */
export default function TemplateMenu({ first }: Props) {
  const { duplicate, pending } = useDashboardActions()
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon" className="size-8" aria-label={`${first.title} actions`}>
          <MoreHorizontalIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuItem disabled={pending} onClick={() => void duplicate(first, { wholeGroup: true })}>
          <CopyIcon />
          Duplicate dashboard
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
