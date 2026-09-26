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
}

/** The title, the switchers, how old the data is, and a refresh for every card that allows one. */
export default function DashboardHeader({ title, children, asOf, refreshable, refreshing, onRefresh }: Props) {
  const hasSwitchers = Children.toArray(children).length > 0
  return (
    <div className="flex flex-col gap-3 sm:flex-row sm:flex-wrap sm:items-center sm:justify-between">
      <h1 className="min-w-0 truncate text-xl font-semibold tracking-tight">{title}</h1>
      <div className="flex flex-wrap items-center gap-2">
        {hasSwitchers && <div className="grid min-w-0 flex-1 auto-cols-fr grid-flow-col gap-2 sm:flex sm:flex-none">{children}</div>}
        <div className="flex items-center gap-1">
          {asOf && (
            <span className="text-xs whitespace-nowrap text-muted-foreground tabular-nums" title={asOf.toLocaleString()}>
              Data as of {formatAsOf(asOf)}
            </span>
          )}
          <Tooltip>
            <TooltipTrigger asChild>
              <span tabIndex={refreshable === 0 ? 0 : -1}>
                <Button
                  variant="ghost"
                  size="icon"
                  className="size-8"
                  aria-label="Refresh all"
                  disabled={refreshable === 0 || refreshing}
                  onClick={onRefresh}
                >
                  <RefreshCwIcon className={refreshing ? 'animate-spin' : undefined} />
                </Button>
              </span>
            </TooltipTrigger>
            <TooltipContent>
              {refreshable === 0
                ? 'Nothing to refresh yet'
                : `Refresh ${refreshable} ${refreshable === 1 ? 'widget' : 'widgets'}`}
            </TooltipContent>
          </Tooltip>
        </div>
      </div>
    </div>
  )
}
