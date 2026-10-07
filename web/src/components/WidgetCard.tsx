import { Suspense, useEffect, useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  ChevronDownIcon,
  CircleAlertIcon,
  CircleOffIcon,
  CloudOffIcon,
  DownloadIcon,
  InboxIcon,
  MoreHorizontalIcon,
  RefreshCwIcon,
  Share2Icon,
  TriangleAlertIcon,
} from 'lucide-react'
import { toast } from 'sonner'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { OffscreenCard } from '@/components/share/OffscreenCard'
import { ShareDialog } from '@/components/share/ShareDialog'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { useNow } from '@/hooks/use-now'
import { useStoredState } from '@/hooks/use-stored-state'
import { ApiError, endpoints, type SqlData, type Widget, type WidgetData, type WidgetDataQuery } from '@/lib/api'
import { captureCard, downloadBlob } from '@/lib/capture'
import { shareCaption, type ShareContext } from '@/lib/share'
import { liveFilters, parseView, type Filter, type TableView } from '@/lib/table-view'
import { formatDuration } from '@/lib/time'
import { canRefresh, componentOf, isRemoteTable, refreshWidget, viewQuery, widgetQuery } from '@/lib/widget-query'
import WidgetFrame from './WidgetFrame'
import WidgetSkeleton from './WidgetSkeleton'

interface Props {
  widget: Widget
  params: WidgetDataQuery
  /** Show what is cached, but ask for nothing (the page is about to change). */
  idle?: boolean
  /** Absent: the card has no menu (Share… and Download PNG). */
  share?: ShareContext
}

/** One widget in its card, loading on its own and showing its own state (D38). */
export default function WidgetCard({ widget, params, idle = false, share }: Props) {
  const client = useQueryClient()
  const stateKey = `twillingate.widget.${widget.dashboard_id}.${widget.widget_id}`
  // The project and range: a view's page, and the answers below, belong to one.
  const selection = JSON.stringify([params.project_id, params.from, params.to])
  const [view, setView] = useTableView(stateKey, selection)
  const remote = isRemoteTable(widget)
  // A remote table's last answer under this selection: its columns decide
  // which filters are sent, and its rows stay on screen while a new view
  // loads or is refused. Another selection's rows never show here.
  const [kept, setKept] = useState<{ selection: string; answer: WidgetData }>()
  const last = kept?.selection === selection ? kept.answer : undefined
  // The selection under which the stored view was refused before any answer
  // named the columns: there, ask once without it, to learn them.
  const [blindFor, setBlindFor] = useState<string>()
  const lastColumns = (last?.data as SqlData | null | undefined)?.columns
  const viewArgs = viewQuery(widget, view, lastColumns ?? (blindFor === selection ? [] : undefined))
  const query = useQuery(widgetQuery(widget, params, idle, viewArgs))
  const settled = remote && !query.isPlaceholderData ? query.data : undefined
  useEffect(() => {
    if (settled) setKept({ selection, answer: settled })
  }, [settled, selection])

  const Component = componentOf(widget)?.default
  const answer = query.data ?? (remote ? last : undefined)
  const refused = query.error instanceof ApiError && query.error.status === 400 ? query.error : undefined
  const sentView = Object.keys(viewArgs).length > 0
  useEffect(() => {
    if (remote && refused && !last && sentView) setBlindFor(selection)
  }, [remote, refused, last, sentView, selection])
  // A remote refusal with rows to keep is the view's fault: it shows under the filter bar.
  const viewError = remote && refused && answer ? refused.message : undefined

  const removed = !Component || answer?.removed === true
  const truncated = (answer?.data as SqlData | null | undefined)?.truncated === true
  const refreshable = widget.source.type === 'sql' && !removed
  const label = widget.title ?? widget.name
  const columns = (answer?.data as SqlData | null | undefined)?.columns ?? []
  // Only the server's filters can empty a remote answer; a local table's empty
  // answer is the whole result, whatever its filters.
  const filtered = remote && liveFilters(view, columns).length > 0

  // Only what is on screen can be shared: not a loading card, a failed one or a removed component.
  const drawable = answer?.data != null && !removed
  const shareable = drawable && !query.isError
  const [sharing, setSharing] = useState(false)
  // Download PNG draws the card out of sight: a new one per click (the key), gone once captured.
  const [downloads, setDownloads] = useState(0)
  const [downloading, setDownloading] = useState(false)
  useEffect(() => {
    // The answer went before the card was drawn: nothing left to capture.
    if (downloading && !drawable) setDownloading(false)
  }, [downloading, drawable])
  const download = async (node: HTMLDivElement) => {
    try {
      const { image2x } = await captureCard(node)
      downloadBlob(image2x, `${widget.name}-${share?.from}-${share?.to}.png`)
    } catch {
      toast.error("Couldn't draw the card")
    } finally {
      setDownloading(false)
    }
  }

  const fetchDistinct = async (column: string, filters: Filter[]) => {
    const others = liveFilters({ ...view, filters }, columns)
    const res = await endpoints.widgetData(widget.widget_id, {
      ...params,
      filters: others.length > 0 ? JSON.stringify(others) : undefined,
      distinct: column,
      limit: undefined,
    })
    const rows = (res.data as SqlData | null)?.rows ?? []
    const capped = res.page !== undefined && res.page.matched > res.page.offset + rows.length
    return rows.map(([value, n]) => ({ value, rows: Number(n), capped }))
  }

  return (
    <WidgetFrame
      title={widget.title}
      wideActions={share !== undefined}
      badge={
        truncated &&
        !remote && (
          <Badge variant="outline" className="min-w-0 shrink text-muted-foreground">
            <span className="truncate">partial: narrow the range or group the query</span>
          </Badge>
        )
      }
      actions={
        (refreshable || share) && (
          <>
            {refreshable && answer && query.isError && !viewError && <StaleWarning error={query.error} />}
            {refreshable && (
              <RefreshButton
                label={`Refresh ${label}`}
                data={answer}
                busy={query.isFetching}
                onRefresh={() => void refreshWidget(client, widget, params, viewArgs).catch(() => {})}
              />
            )}
            {share && (
              <>
                <WidgetMenu
                  canShare={share.writable && share.project !== undefined}
                  disabled={!shareable}
                  downloading={downloading}
                  onShare={() => setSharing(true)}
                  onDownload={() => {
                    setDownloads((n) => n + 1)
                    setDownloading(true)
                  }}
                />
                {/* Both draw into portals: they take no room in the corner. */}
                {share.writable && share.project && drawable && (
                  <ShareDialog
                    open={sharing}
                    onOpenChange={setSharing}
                    widget={widget}
                    data={answer?.data}
                    share={{ ...share, project: share.project }}
                  />
                )}
                {downloading && drawable && (
                  <OffscreenCard
                    key={downloads}
                    component={widget.component ?? ''}
                    data={answer?.data}
                    props={widget.props}
                    title={label}
                    {...shareCaption(widget, share)}
                    onNode={(node) => void download(node)}
                  />
                )}
              </>
            )}
          </>
        )
      }
    >
      {removed ? (
        <CardState icon={<CircleOffIcon />} title="Component removed" />
      ) : answer ? (
        // Data already on screen stays there when a later refetch fails.
        // A remote table filtered to nothing still shows its filters.
        isEmpty(answer) && !filtered ? (
          <CardState icon={<InboxIcon />} title={answer.source_type === 'md' ? 'Nothing to show' : 'No data for this range'} />
        ) : (
          // A lazy component (the map, markdown) keeps the skeleton up while its code loads.
          <Suspense fallback={<WidgetSkeleton widget={widget} />}>
            <Component
              data={answer.data!}
              props={widget.props}
              stateKey={stateKey}
              view={view}
              onView={setView}
              fetchDistinct={remote ? fetchDistinct : undefined}
              page={remote ? answer.page : undefined}
              viewError={viewError}
              reloading={query.isPlaceholderData || (query.isFetching && !!answer)}
            />
          </Suspense>
        )
      ) : query.isError ? (
        <FailedState error={query.error} onRetry={() => query.refetch()} />
      ) : (
        <WidgetSkeleton widget={widget} />
      )}
    </WidgetFrame>
  )
}

/**
 * A table's view: filters and sort kept in this browser per widget, the page
 * in memory only. A change of filters or sort, project or range returns to
 * the first page.
 */
function useTableView(stateKey: string, selection: string): [TableView, (next: TableView) => void] {
  // Runs once, before the stored view is read below.
  useState(() => upgradeStoredSort(stateKey))
  const [stored, setStored] = useStoredState(`${stateKey}.view`, parseView)
  // The offset belongs to the selection it was set under, so a new one reads 0
  // in the same render, before any request for the old page goes out; and is
  // reset, so going back to the old selection starts on the first page too.
  const [paging, setPaging] = useState({ selection, offset: 0 })
  if (paging.selection !== selection) setPaging({ selection, offset: 0 })
  const view: TableView = {
    filters: stored?.filters ?? [],
    sort: stored?.sort ?? null,
    offset: paging.selection === selection ? paging.offset : 0,
  }
  const setView = (next: TableView) => {
    const changed = JSON.stringify([next.filters, next.sort]) !== JSON.stringify([view.filters, view.sort])
    if (changed) setStored(next.filters.length === 0 && next.sort === null ? null : { ...next, offset: 0 })
    setPaging({ selection, offset: changed ? 0 : next.offset })
  }
  return [view, setView]
}

/**
 * Before the card kept the view, a table kept only its sort, under `.sort`.
 * That sort becomes the stored view's, unless a view is already stored, and
 * the old key goes.
 */
function upgradeStoredSort(stateKey: string) {
  try {
    const old = localStorage.getItem(`${stateKey}.sort`)
    if (old === null) return
    const sort = parseView({ sort: JSON.parse(old) })?.sort
    if (sort && localStorage.getItem(`${stateKey}.view`) === null) {
      localStorage.setItem(`${stateKey}.view`, JSON.stringify({ filters: [], sort }))
    }
    localStorage.removeItem(`${stateKey}.sort`)
  } catch {
    // Storage blocked or the old value unreadable: start without it.
  }
}

function isEmpty(answer: WidgetData): boolean {
  const data = answer.data
  if (!data) return true
  // A remote page past the end of a result that has rows is not empty: the
  // table moves back to its last page.
  if (answer.page && answer.page.matched > 0) return false
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
        {/* A button only to take focus, so keyboard users reach the tooltip too. */}
        <button type="button" aria-label={text} className="flex size-7 items-center justify-center text-destructive">
          <CircleAlertIcon className="size-4" />
        </button>
      </TooltipTrigger>
      <TooltipContent>{text}</TooltipContent>
    </Tooltip>
  )
}

interface MenuProps {
  /** Whether Share… is offered (not in reporting dev). */
  canShare: boolean
  /** No answer to draw yet, or none that can be: both actions wait. */
  disabled: boolean
  downloading: boolean
  onShare: () => void
  onDownload: () => void
}

/** The card's "…": Share… (a public link to its picture) and Download PNG. Revealed like the refresh button. */
function WidgetMenu({ canShare, disabled, downloading, onShare, onDownload }: MenuProps) {
  const [open, setOpen] = useState(false)
  return (
    <span className="hover-reveal" data-open={open || undefined}>
      <DropdownMenu open={open} onOpenChange={setOpen}>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" className="size-7" aria-label="Widget actions">
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {canShare && (
            <DropdownMenuItem disabled={disabled} onClick={onShare}>
              <Share2Icon />
              Share…
            </DropdownMenuItem>
          )}
          <DropdownMenuItem disabled={disabled || downloading} onClick={onDownload}>
            <DownloadIcon />
            Download PNG
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </span>
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
