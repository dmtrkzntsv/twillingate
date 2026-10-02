import { Children, type ReactNode } from 'react'
import { RefreshCwIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { formatAsOf } from '@/lib/time'

interface Props {
  title: string
  /** The project and range switchers, whichever the dashboard has. */
  children?: ReactNode
  /** The oldest `cached_at` on screen. */
  asOf?: Date
  /** How many cards are past their `refresh_after`. */
  refreshable: number
  refreshing: boolean
  onRefresh: () => void
  /** The tab's own "…" menu (`TabMenu`), beside the title; the group's sits in the top bar (D11). */
  menu?: ReactNode
}

/**
 * The title with its tab menu, how old the data is and a refresh for
 * every card that allows one on a quiet line beneath, and the switchers
 * on the right.
 */
export default function DashboardHeader({ title, children, asOf, refreshable, refreshing, onRefresh, menu }: Props) {
  const hasSwitchers = Children.toArray(children).length > 0
  const idle = refreshable === 0 || refreshing
  return (
    <div className="flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-center sm:justify-between">
      <div className="flex min-w-0 flex-col gap-0.5">
        <div className="flex min-w-0 items-center gap-1">
          <h1 className="min-w-0 truncate text-2xl font-semibold tracking-tight">{title}</h1>
          {menu}
        </div>
        <div className="flex items-center gap-1 text-xs text-muted-foreground">
          {asOf && (
            <span className="whitespace-nowrap tabular-nums" title={asOf.toLocaleString()}>
              Data as of {formatAsOf(asOf)}
            </span>
          )}
          <Tooltip>
            <TooltipTrigger asChild>
              {/* aria-disabled rather than disabled, so it keeps its focus and its tooltip. */}
              <Button
                variant="ghost"
                size="icon"
                className="size-6 aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
                aria-label="Refresh all"
                aria-disabled={idle || undefined}
                onClick={idle ? undefined : onRefresh}
              >
                <RefreshCwIcon className={`size-3.5 ${refreshing ? 'animate-spin' : ''}`} />
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              {refreshable === 0
                ? 'Nothing to refresh yet'
                : `Refresh ${refreshable} ${refreshable === 1 ? 'widget' : 'widgets'}`}
            </TooltipContent>
          </Tooltip>
        </div>
      </div>
      {hasSwitchers && <div className="grid min-w-0 auto-cols-fr grid-flow-col gap-2 sm:flex">{children}</div>}
    </div>
  )
}
