import type { ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ChevronDownIcon, CircleAlertIcon, CircleOffIcon, CloudOffIcon, InboxIcon, RefreshCwIcon, TriangleAlertIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useNow } from '@/hooks/use-now'
import { ApiError, type SqlData, type Widget, type WidgetData, type WidgetDataQuery } from '@/lib/api'
import { formatDuration } from '@/lib/time'
import { canRefresh, componentOf, refreshWidget, widgetQuery } from '@/lib/widget-query'
import WidgetSkeleton from './WidgetSkeleton'

interface Props {
  widget: Widget
  params: WidgetDataQuery
  /** Show what is cached, but ask for nothing (the page is about to change). */
  idle?: boolean
}

/** One widget in its card, loading on its own and showing its own state (D38). */
export default function WidgetCard({ widget, params, idle = false }: Props) {
  const client = useQueryClient()
  const query = useQuery(widgetQuery(widget, params, idle))
  const Component = componentOf(widget)?.default
  const answer = query.data
  const removed = !Component || answer?.removed === true
  const truncated = (answer?.data as SqlData | null | undefined)?.truncated === true
  const refreshable = widget.source.type === 'sql' && !removed
  const label = widget.title ?? widget.name

  return (
    <Card data-slot="widget-card" className="relative h-full min-w-0 gap-1.5 overflow-hidden p-3.5 shadow-[0_1px_2px_rgb(11_31_54/0.04),0_6px_16px_-10px_rgb(11_31_54/0.14)] dark:shadow-none">
      {(widget.title || truncated) && (
        <div className={`flex min-h-7 min-w-0 items-center gap-2 ${refreshable ? 'pr-8' : ''}`}>
          {widget.title && <h3 className="truncate text-sm font-medium text-muted-foreground">{widget.title}</h3>}
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
        ) : answer ? (
          // Data already on screen stays there when a later refetch fails.
          isEmpty(answer) ? (
            <CardState icon={<InboxIcon />} title={answer.source_type === 'md' ? 'Nothing to show' : 'No data for this range'} />
          ) : (
            <Component data={answer.data!} props={widget.props} />
          )
        ) : query.isError ? (
          <FailedState error={query.error} onRetry={() => query.refetch()} />
        ) : (
          <WidgetSkeleton widget={widget} />
        )}
      </div>
      {refreshable && (
        <div className="absolute top-2.5 right-2.5 flex items-center">
          {answer && query.isError && <StaleWarning error={query.error} />}
          <RefreshButton
            label={`Refresh ${label}`}
            data={answer}
            busy={query.isFetching}
            onRefresh={() => void refreshWidget(client, widget, params).catch(() => {})}
          />
        </div>
      )}
    </Card>
  )
}

function isEmpty(answer: WidgetData): boolean {
  const data = answer.data
  if (!data) return true
  // A defensive `?? []`: the server always sends a rows array (a query
  // that matches nothing is still `[]`, never absent), but this stays cheap
  // insurance against ever crashing the whole page on one bad answer.
  if ('rows' in data) return (data.rows ?? []).length === 0
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

/** A refetch failed while older data stays on screen: say so without taking the card over. */
function StaleWarning({ error }: { error: Error }) {
  const text = `Couldn't refresh: ${error.message}`
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span tabIndex={0} role="img" aria-label={text} className="flex size-7 items-center justify-center text-destructive">
          <CircleAlertIcon className="size-4" />
        </span>
      </TooltipTrigger>
      <TooltipContent>{text}</TooltipContent>
    </Tooltip>
  )
}

function refreshHint(data: WidgetData | undefined, now: number): string {
  if (!data?.cached_at || !data.refresh_after) return 'Refresh'
  const age = `Updated ${formatDuration(now - Date.parse(data.cached_at))} ago`
  const wait = Date.parse(data.refresh_after) - now
  return wait > 0 ? `${age} · can refresh in ${formatDuration(wait)}` : `${age} · refresh now`
}

interface RefreshProps {
  label: string
  data?: WidgetData
  busy: boolean
  onRefresh: () => void
}

/**
 * Asks for `fresh=true` (D39). Shown on hover (always on touch screens, see
 * `.hover-reveal`). Until the answer's `refresh_after` it is `aria-disabled`
 * rather than `disabled`, so it keeps its focus and its tooltip.
 */
function RefreshButton({ label, data, busy, onRefresh }: RefreshProps) {
  const now = useNow()
  const ready = canRefresh(data, now) && !busy
  return (
    // The wrapper carries the reveal, so the button's own disabled look is not overridden.
    // A first load already shows its skeleton, so only a refetch keeps it in view.
    <span className="hover-reveal" data-busy={(busy && data !== undefined) || undefined}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            variant="ghost"
            size="icon"
            className="size-7 aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
            aria-label={label}
            aria-disabled={!ready || undefined}
            onClick={ready ? onRefresh : undefined}
          >
            <RefreshCwIcon className={busy ? 'animate-spin' : undefined} />
          </Button>
        </TooltipTrigger>
        <TooltipContent>{refreshHint(data, now)}</TooltipContent>
      </Tooltip>
    </span>
  )
}
