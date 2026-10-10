import { lazy, Suspense, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { TriangleAlertIcon } from 'lucide-react'
import RangeSwitcher, { type RangeValue } from '@/components/RangeSwitcher'
import WidgetFrame from '@/components/WidgetFrame'
import { Alert, AlertDescription } from '@/components/ui/alert'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Kbd } from '@/components/ui/kbd'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { widgets } from '@/components/widgets'
import { ApiError, endpoints, type Widget } from '@/lib/api'
import { projectsQuery, schemaViewsQuery } from '@/lib/queries'
import { resolve } from '@/lib/ranges'
import { carryProps, needsHint } from '@/lib/props-form'
import { parseSchemaViews } from '@/lib/schema-views'
import { draftOf, isDirty, sourceOf, sourceTypeOf, usesParams, type Draft } from '@/lib/widget-draft'
import { iconOf } from './component-icons'
import PropsForm from './PropsForm'
import ResultsPane, { RUN_KEYS, type RunState } from './ResultsPane'

/**
 * Whether Escape belongs to the SQL editor rather than the dialog: with
 * its completion list open, Escape closes the list (CodeEditor does that
 * once the dialog has prevented the default) and the dialog stays.
 */
export function shouldKeepOpenOnEscape(target: EventTarget | null, doc: Document): boolean {
  const inEditor = target instanceof Element && target.closest('.cm-editor') !== null
  return inEditor && doc.querySelector('.cm-tooltip-autocomplete') !== null
}

// CodeMirror loads with the editor, in its own chunk.
const CodeEditor = lazy(() => import('./CodeEditor'))

/** What the editor runs the query with until the user picks otherwise: the dashboard's own selection. */
export interface EditorDefaults {
  projectId?: number
  range: RangeValue
  timezone: string
}

export interface WidgetEditorProps {
  open: boolean
  onOpenChange(open: boolean): void
  /** Absent: adding a widget. */
  widget?: Widget
  defaults: EditorDefaults
  /** Saves the draft; resolves on success (the modal closes), throws ApiError/Error to show its message in the banner. */
  onSave(draft: Draft): Promise<void>
}

const known = (c: string | null): c is string => c !== null && Object.hasOwn(widgets, c)

/** The draft an editor opens on; a removed component opens with the visualization empty (spec D16). */
function startDraft(widget?: Widget): Draft {
  const d = draftOf(widget)
  return known(d.component) ? d : { ...d, component: null }
}

function messageOf(err: unknown): string {
  if (err instanceof ApiError) return err.message
  if (err instanceof TypeError) return "Couldn't reach the server"
  return err instanceof Error ? err.message : String(err)
}

/** The modal that adds or edits one widget (spec D9–D16); every open starts from the widget as stored. */
export default function WidgetEditor(p: WidgetEditorProps) {
  const [opened, setOpened] = useState({ open: p.open, n: 0 })
  if (p.open !== opened.open) setOpened({ open: p.open, n: opened.n + (p.open ? 1 : 0) })
  return <Editor key={`${opened.n}:${p.widget?.widget_id ?? 'new'}`} {...p} />
}

function Editor({ open, onOpenChange, widget, defaults, onSave }: WidgetEditorProps) {
  const [draft, setDraft] = useState(() => startDraft(widget))
  const edit = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }))

  const projects = useQuery(projectsQuery)
  const schema = useQuery(schemaViewsQuery)
  const tables = useMemo(() => (schema.data ? parseSchemaViews(schema.data) : undefined), [schema.data])
  const live = (projects.data?.projects ?? []).filter((x) => !x.archived)
  const [picked, setPicked] = useState(defaults.projectId)
  const projectId = picked ?? live[0]?.project_id
  const [range, setRange] = useState(defaults.range)

  const [run, setRun] = useState<RunState>({ kind: 'idle' })
  const [ranWith, setRanWith] = useState<string | null>(null)
  const running = useRef(false)
  const [propsValid, setPropsValid] = useState(true)
  const [saving, setSaving] = useState(false)
  const savingNow = useRef(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [confirmDiscard, setConfirmDiscard] = useState(false)

  const component = known(draft.component) ? draft.component : null
  const module = component ? widgets[component] : undefined
  const markdown = sourceTypeOf(component) === 'md'
  const uses = usesParams(draft.sql)
  // The dashboard hands over its range resolved; a preset picked here is resolved now.
  const span = range.from && range.to ? { from: range.from, to: range.to } : resolve(range.range, defaults.timezone, new Date())
  // What a run reads: a change to any of it after the run dims its rows.
  const runKey = JSON.stringify([draft.sql, uses.project ? projectId : null, uses.range ? span : null])
  const stale = ranWith !== null && ranWith !== runKey
  const last = run.kind === 'done' ? run.result : run.kind === 'running' ? run.last : undefined

  const runQuery = async () => {
    if (!component || markdown || running.current) return
    running.current = true
    setRanWith(runKey)
    setRun((r) => ({ kind: 'running', last: r.kind === 'done' ? r.result : r.kind === 'running' ? r.last : undefined }))
    try {
      const result = await endpoints.previewWidget({
        component,
        props: draft.props,
        source: { type: 'sql', content: draft.sql },
        ...(uses.project && projectId !== undefined ? { project_id: projectId } : {}),
        ...(uses.range ? span : {}),
      })
      setRun({ kind: 'done', result })
    } catch (err) {
      setRun({ kind: 'failed', message: messageOf(err) })
    } finally {
      running.current = false
    }
  }

  const dirty = isDirty(widget, draft)
  const hasSource = (sourceOf(draft)?.content.trim() ?? '') !== ''
  const canSave = component !== null && propsValid && !saving && (widget ? dirty : hasSource)

  const save = async () => {
    if (!canSave || savingNow.current) return
    savingNow.current = true
    setSaving(true)
    setSaveError(null)
    try {
      await onSave(draft)
      onOpenChange(false)
    } catch (err) {
      setSaveError(messageOf(err))
    } finally {
      savingNow.current = false
      setSaving(false)
    }
  }

  // ✕, Escape, a click outside and Cancel all come here.
  const close = () => (dirty ? setConfirmDiscard(true) : onOpenChange(false))

  const sqlControls = (
    <div className="flex min-w-0 flex-wrap items-center gap-2">
      <span className="mr-auto text-sm font-medium">SQL</span>
      {uses.project && (
        <Select value={projectId === undefined ? '' : String(projectId)} onValueChange={(v) => setPicked(Number(v))}>
          <SelectTrigger size="sm" aria-label="Project" className="max-w-60 min-w-0">
            <SelectValue placeholder="Project" />
          </SelectTrigger>
          <SelectContent>
            {live.map((x) => (
              <SelectItem key={x.project_id} value={String(x.project_id)}>
                {x.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      )}
      {uses.range && <RangeSwitcher value={range} timezone={defaults.timezone} onChange={setRange} />}
    </div>
  )

  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent
        className="flex max-h-[85vh] w-full flex-col gap-4 sm:max-w-[1100px] max-sm:h-dvh max-sm:max-h-dvh max-sm:max-w-none max-sm:rounded-none"
        onEscapeKeyDown={(e) => shouldKeepOpenOnEscape(e.target, document) && e.preventDefault()}
      >
        <form
          className="contents"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <DialogHeader className="min-w-0 pr-8 text-left">
            <DialogTitle>{widget ? 'Edit widget' : 'Add widget'}</DialogTitle>
            <DialogDescription className="sr-only">Choose a visualization, write its query and save the widget.</DialogDescription>
            <Input aria-label="Title" placeholder="Title" value={draft.title} onChange={(e) => edit({ title: e.target.value })} />
          </DialogHeader>
          <div className="grid min-h-0 flex-1 grid-cols-1 gap-4 overflow-y-auto lg:grid-cols-[minmax(0,320px)_minmax(0,1fr)]">
            <div className="flex min-w-0 flex-col gap-4">
              <div className="grid min-w-0 grid-cols-1 gap-1.5">
                <Label htmlFor="widget-component">Visualization</Label>
                <Select
                  value={component ?? ''}
                  onValueChange={(next) =>
                    setDraft((d) => ({ ...d, component: next, props: carryProps(d.props, widgets[next].contract.props) }))
                  }
                >
                  <SelectTrigger id="widget-component" className="w-full min-w-0">
                    <SelectValue placeholder="Choose a visualization" />
                  </SelectTrigger>
                  <SelectContent>
                    {Object.keys(widgets)
                      .sort()
                      .map((name) => {
                        const Icon = iconOf(name)
                        return (
                          <SelectItem key={name} value={name}>
                            <Icon />
                            {name}
                          </SelectItem>
                        )
                      })}
                  </SelectContent>
                </Select>
                {module && <p className="text-xs text-muted-foreground">{needsHint(module.contract)}</p>}
              </div>
              {component && (
                <PropsForm
                  component={component}
                  value={draft.props}
                  onChange={(props) => edit({ props })}
                  columns={last?.columns ?? []}
                  onValidity={setPropsValid}
                />
              )}
            </div>
            <div className="flex min-w-0 flex-col gap-3">
              {markdown ? (
                <div className="grid min-w-0 grid-cols-1 gap-3 lg:grid-cols-2">
                  <div className="h-48 min-w-0 lg:h-72">
                    <Suspense fallback={<Skeleton className="h-full" />}>
                      <CodeEditor language="markdown" label="Markdown" value={draft.markdown} onChange={(md) => edit({ markdown: md })} />
                    </Suspense>
                  </div>
                  <MarkdownPreview text={draft.markdown} />
                </div>
              ) : (
                <>
                  {sqlControls}
                  <div className="h-48 min-w-0 lg:h-56">
                    <Suspense fallback={<Skeleton className="h-full" />}>
                      <CodeEditor
                        language="sql"
                        label="SQL"
                        value={draft.sql}
                        onChange={(sql) => edit({ sql })}
                        onRun={() => void runQuery()}
                        tables={tables}
                      />
                    </Suspense>
                  </div>
                  <div className="flex min-w-0 flex-wrap items-center gap-2">
                    <Button type="button" variant="outline" disabled={!component || run.kind === 'running'} onClick={() => void runQuery()}>
                      Run query <Kbd>{RUN_KEYS}</Kbd>
                    </Button>
                    {!component && <span className="text-xs text-muted-foreground">Choose a visualization to run</span>}
                  </div>
                  <ResultsPane
                    component={component}
                    props={draft.props}
                    height={widget?.height ?? module?.contract.defaultHeight ?? 8}
                    state={run}
                    stale={stale}
                  />
                </>
              )}
            </div>
          </div>
          {saveError && (
            <Alert variant="destructive">
              <TriangleAlertIcon />
              <AlertDescription className="break-words whitespace-pre-wrap">{saveError}</AlertDescription>
            </Alert>
          )}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" disabled={!canSave}>
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
      <AlertDialog open={confirmDiscard} onOpenChange={setConfirmDiscard}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Discard changes?</AlertDialogTitle>
            <AlertDialogDescription>What you changed in this widget is not saved.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Keep editing</AlertDialogCancel>
            <AlertDialogAction onClick={() => onOpenChange(false)}>Discard</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </Dialog>
  )
}

/** A markdown widget's text as the dashboard draws it, beside its editor (spec D15). */
function MarkdownPreview({ text }: { text: string }) {
  const Markdown = widgets.markdown.default
  return (
    <div className="h-48 min-w-0 lg:h-72">
      <WidgetFrame>
        <Suspense fallback={<Skeleton className="h-full" />}>
          <Markdown data={{ markdown: text }} props={{}} />
        </Suspense>
      </WidgetFrame>
    </div>
  )
}
