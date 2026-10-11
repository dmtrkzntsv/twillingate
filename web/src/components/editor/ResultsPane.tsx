import { Suspense, type ReactNode } from 'react'
import { InboxIcon, TriangleAlertIcon } from 'lucide-react'
import WidgetErrorBoundary from '@/components/WidgetErrorBoundary'
import WidgetFrame from '@/components/WidgetFrame'
import WidgetSkeleton from '@/components/WidgetSkeleton'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Kbd } from '@/components/ui/kbd'
import { Spinner } from '@/components/ui/spinner'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { widgets } from '@/components/widgets'
import type { PreviewResult } from '@/lib/api'
import { rowsPx } from '@/lib/grid'
import { cn } from '@/lib/utils'

/** A run's rows, with the visualization and props the server checked them against. */
export interface Ran {
  result: PreviewResult
  component: string
  props: Record<string, unknown>
}

/** Where the editor's query stands: never run, running (over the last answer), answered, or refused. */
export type RunState =
  | { kind: 'idle' }
  | { kind: 'running'; last?: Ran }
  | ({ kind: 'done' } & Ran)
  | { kind: 'failed'; message: string }

/** The answer on screen: the last one, kept while the next run is out. */
export function ranOf(state: RunState): Ran | undefined {
  if (state.kind === 'done') return { result: state.result, component: state.component, props: state.props }
  return state.kind === 'running' ? state.last : undefined
}

/** The keys that run the query, as this platform names them. */
export const RUN_KEYS = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.userAgent) ? '⌘↵' : 'Ctrl ↵'

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? '' : 's'}`

interface PreviewProps {
  component: string | null
  props: Record<string, unknown>
  /** The draft's title, shown as the dashboard shows it. */
  title: string
  /** The widget's height in grid rows: the preview is drawn in a frame that tall. */
  height: number
  state: RunState
  /** The query changed after the run these rows came from. */
  stale: boolean
}

/**
 * The chosen component drawn from the last run's rows, as the dashboard
 * draws it (spec D13), with the run's warning beneath. A change of
 * component or props redraws from the same rows at once; the warning
 * waits for the run that checks them.
 */
export function Preview({ component, props, title, height, state, stale }: PreviewProps) {
  const ran = ranOf(state)
  const result = ran?.result
  const module = component && Object.hasOwn(widgets, component) ? widgets[component] : undefined
  const Component = module?.default
  // A remote table pages through the server; here it has only these rows.
  const drawn = component === 'table' ? { ...props, mode: undefined } : props
  // The run's warning is about the visualization it was checked against.
  const checked = ran !== undefined && ran.component === component && JSON.stringify(ran.props) === JSON.stringify(props)
  const problem = checked ? result?.problem : undefined
  const dim = state.kind === 'running' || stale || !checked

  let body
  if (!Component) body = <Placeholder>Choose a visualization to preview the rows</Placeholder>
  else if (state.kind === 'failed') body = <Placeholder>The query was refused, so there is nothing to draw</Placeholder>
  else if (state.kind === 'idle')
    body = (
      <Placeholder>
        Run the query to see the preview <Kbd>{RUN_KEYS}</Kbd>
      </Placeholder>
    )
  else if (!result) body = <WidgetSkeleton widget={{ component, props: drawn }} />
  // As the dashboard's card says it, rather than a component drawn from nothing.
  else if (result.rows.length === 0)
    body = (
      <CardState icon={<InboxIcon />} dim={dim}>
        The query returned no rows
      </CardState>
    )
  else
    body = (
      <div className={cn('h-full min-w-0 transition-opacity', dim && 'opacity-60')}>
        {/* Rows that do not fit the component can make it throw: that stays in the preview. */}
        <WidgetErrorBoundary
          resetKeys={[component, result, props]}
          fallback={
            <CardState icon={<TriangleAlertIcon />} detail={problem}>
              This visualization can't draw these rows
            </CardState>
          }
        >
          <Suspense fallback={<WidgetSkeleton widget={{ component, props: drawn }} />}>
            <Component data={result} props={drawn} />
          </Suspense>
        </WidgetErrorBoundary>
      </div>
    )

  return (
    <section aria-label="Preview" className="flex min-w-0 flex-col gap-3">
      <div className="min-w-0" style={{ height: rowsPx(height) }}>
        <WidgetFrame title={title}>{body}</WidgetFrame>
      </div>
      {problem && (
        <Alert>
          <TriangleAlertIcon />
          <AlertDescription className="break-words">{problem}</AlertDescription>
        </Alert>
      )}
    </section>
  )
}

/** Where the preview would be, saying why it is not. */
function Placeholder({ children }: { children: ReactNode }) {
  return <div className="flex h-full items-center justify-center p-4 text-center text-sm text-muted-foreground">{children}</div>
}

/** In place of a preview: no rows, or rows the component threw on. */
function CardState({ icon, detail, dim, children }: { icon: ReactNode; detail?: string; dim?: boolean; children: ReactNode }) {
  return (
    <div
      className={cn(
        'flex h-full flex-col items-center justify-center gap-1.5 p-2 text-center text-muted-foreground [&>svg]:size-5 [&>svg]:shrink-0',
        dim && 'opacity-60'
      )}
    >
      {icon}
      <p className="text-sm font-medium">{children}</p>
      {detail && <p className="text-xs break-words">{detail}</p>}
    </div>
  )
}

/** The last run in a line: rows, time, and whether the query moved on since. */
export function RunStatus({ state, stale }: { state: RunState; stale: boolean }) {
  const result = ranOf(state)?.result
  if (state.kind === 'idle' || state.kind === 'failed') return null
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground">
      {state.kind === 'running' && <Spinner />}
      {result ? (
        <span>
          {plural(result.rows.length, 'row')} · {result.elapsed_ms} ms
          {result.truncated && ` · truncated to ${plural(result.rows.length, 'row')}`}
        </span>
      ) : (
        <span>Running…</span>
      )}
      {stale && <span className="font-medium text-foreground">Changed since last run</span>}
    </div>
  )
}

/** The last run's rows as a table, filling the space it is given; a refusal in their place (spec D13). */
export function Rows({ state, stale }: { state: RunState; stale: boolean }) {
  const result = ranOf(state)?.result
  if (state.kind === 'idle') {
    return (
      <p className="p-4 text-sm text-muted-foreground">
        Run the query to see its rows <Kbd>{RUN_KEYS}</Kbd>
      </p>
    )
  }
  if (state.kind === 'failed') {
    return (
      <div className="p-4">
        <Alert variant="destructive">
          <TriangleAlertIcon />
          <AlertDescription className="break-words whitespace-pre-wrap">{state.message}</AlertDescription>
        </Alert>
      </div>
    )
  }
  if (!result) return null
  return (
    <section
      aria-label="Rows"
      // The table's own box scrolls both ways, so its header row stays in view.
      className={cn(
        'flex max-h-80 min-h-0 min-w-0 flex-col transition-opacity lg:max-h-none lg:flex-1',
        '[&_[data-slot=table-container]]:min-h-0 [&_[data-slot=table-container]]:flex-1 [&_[data-slot=table-container]]:overflow-auto',
        (state.kind === 'running' || stale) && 'opacity-60'
      )}
    >
      <Table>
        <TableHeader className="sticky top-0 z-10 bg-card shadow-[inset_0_-1px_0_var(--border)]">
          <TableRow>
            {result.columns.map((c, i) => (
              <TableHead key={i} className="px-4 text-muted-foreground">
                {c}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {result.rows.length === 0 && (
            <TableRow>
              <TableCell colSpan={Math.max(result.columns.length, 1)} className="px-4 text-muted-foreground">
                No rows
              </TableCell>
            </TableRow>
          )}
          {result.rows.map((row, r) => (
            <TableRow key={r}>
              {row.map((cell, i) => (
                <TableCell key={i} className="px-4 whitespace-nowrap tabular-nums">
                  {cell}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </section>
  )
}
