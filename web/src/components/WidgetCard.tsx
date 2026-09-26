import type { ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDownIcon, CircleOffIcon, CloudOffIcon, InboxIcon, RefreshCwIcon, TriangleAlertIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Skeleton } from '@/components/ui/skeleton'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { widgets } from '@/components/widgets'
import { useNow } from '@/hooks/use-now'
import { ApiError, type SqlData, type Widget, type WidgetData, type WidgetDataQuery } from '@/lib/api'
import { formatDuration } from '@/lib/time'
import { canRefresh, refreshWidget, widgetQuery } from '@/lib/widget-query'

interface Props {
  widget: Widget
  params: WidgetDataQuery
}

/** One widget in its card, loading on its own and showing its own state (D38). */
export default function WidgetCard({ widget, params }: Props) {
  const client = useQueryClient()
  const query = useQuery(widgetQuery(widget, params))
  const Component = widget.component === null ? undefined : widgets[widget.component]?.default
  const removed = !Component || query.data?.removed === true
  const truncated = (query.data?.data as SqlData | null | undefined)?.truncated === true
  const refreshable = widget.source.type === 'sql' && !removed

  return (
    <Card data-slot="widget-card" className="relative h-full min-w-0 gap-1 overflow-hidden p-3 shadow-xs">
      {(widget.title || truncated) && (
        <div className={`flex min-h-7 min-w-0 items-center gap-2 ${refreshable ? 'pr-8' : ''}`}>
          {widget.title && <h3 className="truncate text-sm font-medium">{widget.title}</h3>}
          {truncated && (
            <Badge variant="outline" className="min-w-0 shrink text-muted-foreground">
              <span className="truncate">partial: narrow the range or group the query</span>
            </Badge>
          )}
        </div>
      )}
      <div className="relative min-h-0 flex-1 overflow-auto">
        {removed ? (
          <CardState icon={<CircleOffIcon />} title="Component removed" />
        ) : query.isError ? (
          <FailedState error={query.error} onRetry={() => query.refetch()} />
        ) : query.isPending ? (
          <Skeleton className="h-full w-full" />
        ) : isEmpty(query.data) ? (
          <CardState icon={<InboxIcon />} title="No data for this range" />
        ) : (
          <Component data={query.data.data!} props={widget.props} />
        )}
      </div>
      {refreshable && (
        <RefreshButton
          data={query.data}
          busy={query.isFetching}
          onRefresh={() => void refreshWidget(client, widget, params).catch(() => {})}
        />
      )}
    </Card>
  )
}

function isEmpty(answer: WidgetData): boolean {
  const data = answer.data
  if (!data) return true
  if ('rows' in data) return data.rows.length === 0
  return data.markdown.trim() === ''
}

function CardState({ icon, title, children }: { icon: ReactNode; title: string; children?: ReactNode }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-1.5 p-2 text-center text-muted-foreground [&>svg]:size-5 [&>svg]:shrink-0">
      {icon}
      <p className="text-sm font-medium">{title}</p>
      {children}
    </div>
  )
}

function FailedState({ error, onRetry }: { error: Error; onRetry: () => void }) {
  // A 400 is the API refusing the query itself (a renamed column, a dropped
  // view): retrying cannot help, so show why instead of offering to.
  if (error instanceof ApiError && error.status === 400) {
    return (
      <CardState icon={<TriangleAlertIcon />} title="Query no longer runs">
        <Collapsible className="flex w-full flex-col items-center">
          <CollapsibleTrigger asChild>
            <Button variant="link" size="sm" className="group h-6 text-muted-foreground">
              Details
              <ChevronDownIcon className="transition-transform group-data-[state=open]:rotate-180" />
            </Button>
          </CollapsibleTrigger>
          <CollapsibleContent className="w-full">
            <pre className="rounded-md bg-muted p-2 text-left font-mono text-xs break-words whitespace-pre-wrap text-foreground">
              {error.message}
            </pre>
          </CollapsibleContent>
        </Collapsible>
      </CardState>
    )
  }
  return (
    <CardState icon={<CloudOffIcon />} title="Couldn't load">
      <Button variant="outline" size="sm" className="mt-1 h-7" onClick={onRetry}>
        Retry
      </Button>
    </CardState>
  )
}

function refreshHint(data: WidgetData | undefined, now: number): string {
  if (!data?.cached_at || !data.refresh_after) return 'Refresh'
  const age = `Updated ${formatDuration(now - Date.parse(data.cached_at))} ago`
  const wait = Date.parse(data.refresh_after) - now
  return wait > 0 ? `${age} · can refresh in ${formatDuration(wait)}` : `${age} · refresh now`
}

/**
 * Asks for `fresh=true` (D39). Shown on hover (always on touch screens, see
 * `.hover-reveal`), and disabled until the answer's `refresh_after`.
 */
function RefreshButton({ data, busy, onRefresh }: { data?: WidgetData; busy: boolean; onRefresh: () => void }) {
  const now = useNow()
  const ready = canRefresh(data, now) && !busy
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        {/* A disabled button fires no pointer events; the wrapper keeps the tooltip. */}
        <span className="hover-reveal absolute top-2.5 right-2.5" data-busy={busy || undefined} tabIndex={ready ? -1 : 0}>
          <Button variant="ghost" size="icon" className="size-7" aria-label="Refresh" disabled={!ready} onClick={onRefresh}>
            <RefreshCwIcon className={busy ? 'animate-spin' : undefined} />
          </Button>
        </span>
      </TooltipTrigger>
      <TooltipContent>{refreshHint(data, now)}</TooltipContent>
    </Tooltip>
  )
}
