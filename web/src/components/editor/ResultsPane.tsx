import { Suspense, useState } from 'react'
import { TriangleAlertIcon } from 'lucide-react'
import WidgetErrorBoundary from '@/components/WidgetErrorBoundary'
import WidgetFrame from '@/components/WidgetFrame'
import WidgetSkeleton from '@/components/WidgetSkeleton'
import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Kbd } from '@/components/ui/kbd'
import { Spinner } from '@/components/ui/spinner'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
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

interface Props {
  component: string | null
  props: Record<string, unknown>
  /** The widget's height in grid rows: the preview is drawn in a frame that tall. */
  height: number
  state: RunState
  /** The query changed after the run these rows came from. */
  stale: boolean
}

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? '' : 's'}`

/** The last run's rows: the chosen component drawn from them, and the rows as a table (spec D13). */
export default function ResultsPane({ component, props, height, state, stale }: Props) {
  const ran = ranOf(state)
  const result = ran?.result
  // Each new answer opens on Preview.
  const [tab, setTab] = useState('preview')
  const [shown, setShown] = useState(result)
  if (result !== shown) {
    setShown(result)
    if (result) setTab('preview')
  }

  if (state.kind === 'idle') {
    return (
      <p className="text-sm text-muted-foreground">
        Run the query to see its rows <Kbd>{RUN_KEYS}</Kbd>
      </p>
    )
  }
  if (state.kind === 'failed') {
    return (
      <Alert variant="destructive">
        <TriangleAlertIcon />
        <AlertDescription className="break-words whitespace-pre-wrap">{state.message}</AlertDescription>
      </Alert>
    )
  }

  const running = state.kind === 'running'
  const module = component && Object.hasOwn(widgets, component) ? widgets[component] : undefined
  const Component = module?.default
  // A remote table pages through the server; here it has only these rows.
  const drawn = component === 'table' ? { ...props, mode: undefined } : props
  const dim = running || stale ? 'opacity-60' : ''
  // The run's warning is about the visualization it was checked against: another one waits for the next run.
  const unchecked =
    ran !== undefined && component !== null && (ran.component !== component || JSON.stringify(ran.props) !== JSON.stringify(props))
  const problem = unchecked ? undefined : result?.problem

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-sm text-muted-foreground">
        {running && <Spinner />}
        {result ? (
          <span>
            {plural(result.rows.length, 'row')} · {result.elapsed_ms} ms
            {result.truncated && ` · truncated to ${plural(result.rows.length, 'row')}`}
          </span>
        ) : (
          <span>Running…</span>
        )}
        {stale && <span className="font-medium text-foreground">Changed since last run</span>}
        {unchecked && <span className="font-medium text-foreground">Run again to check the rows against {component}</span>}
      </div>
      {problem && (
        <Alert>
          <TriangleAlertIcon />
          <AlertDescription className="break-words">{problem}</AlertDescription>
        </Alert>
      )}
      {result && (
        <Tabs value={tab} onValueChange={setTab} className="min-w-0">
          <TabsList>
            <TabsTrigger value="preview">Preview</TabsTrigger>
            <TabsTrigger value="table">Table</TabsTrigger>
          </TabsList>
          <TabsContent value="preview" className={cn('min-w-0', dim)}>
            {Component ? (
              <div className="min-w-0" style={{ height: rowsPx(height) }}>
                <WidgetFrame>
                  {/* Rows that do not fit the component can make it throw: that stays in the preview. */}
                  <WidgetErrorBoundary
                    resetKeys={[component, result, props]}
                    fallback={<CannotDraw problem={problem} onTable={() => setTab('table')} />}
                  >
                    <Suspense fallback={<WidgetSkeleton widget={{ component, props: drawn }} />}>
                      <Component data={result} props={drawn} />
                    </Suspense>
                  </WidgetErrorBoundary>
                </WidgetFrame>
              </div>
            ) : (
              <p className="text-sm text-muted-foreground">Choose a visualization to preview the rows</p>
            )}
          </TabsContent>
          <TabsContent value="table" className={cn('min-w-0', dim)}>
            <div className="max-h-80 min-w-0 overflow-auto rounded-md border">
              <Table>
                <TableHeader>
                  <TableRow>
                    {result.columns.map((c, i) => (
                      <TableHead key={i}>{c}</TableHead>
                    ))}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {result.rows.map((row, r) => (
                    <TableRow key={r}>
                      {row.map((cell, i) => (
                        <TableCell key={i} className="whitespace-nowrap">
                          {cell}
                        </TableCell>
                      ))}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          </TabsContent>
        </Tabs>
      )}
    </div>
  )
}

/** In place of a preview that threw on these rows. */
function CannotDraw({ problem, onTable }: { problem?: string; onTable: () => void }) {
  return (
    <div className="flex h-full flex-col items-center justify-center gap-1.5 p-2 text-center text-muted-foreground [&>svg]:size-5 [&>svg]:shrink-0">
      <TriangleAlertIcon />
      <p className="text-sm font-medium">This visualization can't draw these rows</p>
      {problem && <p className="text-xs break-words">{problem}</p>}
      <Button type="button" variant="link" size="sm" className="h-6" onClick={onTable}>
        See them as a table
      </Button>
    </div>
  )
}
