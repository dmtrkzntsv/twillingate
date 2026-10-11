import { lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { PlayIcon, TriangleAlertIcon } from 'lucide-react'
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
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Kbd } from '@/components/ui/kbd'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { widgets } from '@/components/widgets'
import { ApiError, endpoints, type Widget } from '@/lib/api'
import { rowsPx } from '@/lib/grid'
import { projectsQuery, schemaViewsQuery } from '@/lib/queries'
import { resolve } from '@/lib/ranges'
import { carryProps, needsHint } from '@/lib/props-form'
import { parseSchemaViews } from '@/lib/schema-views'
import { draftOf, isDirty, sourceOf, sourceTypeOf, usesParams, type Draft } from '@/lib/widget-draft'
import { iconOf } from './component-icons'
import PropsForm, { SettingRow } from './PropsForm'
import { Preview, ranOf, Rows, RUN_KEYS, RunStatus, type RunState } from './ResultsPane'

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

/** How long a change of visualization, props, project or range waits before it runs the query. */
const AUTO_RUN_MS = 400

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
  // What the last run read, and its SQL.
  const [ranWith, setRanWith] = useState<{ key: string; sql: string } | null>(null)
  // Runs overlap (a key press while an automatic one is out): only the latest one's answer shows.
  const latest = useRef(0)
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
  const stale = ranWith !== null && ranWith.key !== runKey
  const last = ranOf(run)?.result
  // What an automatic run reads; null while there is nothing it could run.
  const autoKey =
    component && !markdown && draft.sql.trim() !== '' && (!uses.project || projectId !== undefined)
      ? JSON.stringify([component, draft.props, uses.project ? projectId : null, uses.range ? span : null])
      : null
  const ranAuto = useRef<string | null>(null)
  const everRan = ranWith !== null
  const editing = widget !== undefined
  // An automatic run never runs SQL typed since the last run: that waits for ⌘↵.
  const autoDue = autoKey !== null && autoKey !== ranAuto.current && (everRan ? ranWith.sql === draft.sql : editing)

  const runQuery = async (auto = false) => {
    if (!component || markdown) return
    // Checked again as it fires: a run since it was scheduled may have made it moot.
    if (auto && !autoDue) return
    const n = ++latest.current
    ranAuto.current = autoKey
    setRanWith({ key: runKey, sql: draft.sql })
    setRun((r) => ({ kind: 'running', last: ranOf(r) }))
    const props = draft.props
    try {
      const result = await endpoints.previewWidget({
        component,
        props,
        source: { type: 'sql', content: draft.sql },
        ...(uses.project && projectId !== undefined ? { project_id: projectId } : {}),
        ...(uses.range ? span : {}),
      })
      if (n === latest.current) setRun({ kind: 'done', result, component, props })
    } catch (err) {
      if (n === latest.current) setRun({ kind: 'failed', message: messageOf(err) })
    }
  }

  // The preview shows without asking: an edit runs the stored query as it
  // opens, and once anything has run, a change of visualization, props,
  // project or range runs it again. Typing SQL never does; ⌘↵ does.
  const runNow = useRef(runQuery)
  runNow.current = runQuery
  useEffect(() => {
    if (!autoDue) return
    if (!everRan) {
      void runNow.current(true)
      return
    }
    const t = setTimeout(() => void runNow.current(true), AUTO_RUN_MS)
    return () => clearTimeout(t)
  }, [autoDue, autoKey, everRan])

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

  // Escape and Cancel both come here.
  const close = () => (dirty ? setConfirmDiscard(true) : onOpenChange(false))
  const height = widget?.height ?? module?.contract.defaultHeight ?? 8

  const projectPicker = uses.project && (
    <Select value={projectId === undefined ? '' : String(projectId)} onValueChange={(v) => setPicked(Number(v))}>
      <SelectTrigger size="sm" aria-label="Project" className="max-w-48 min-w-0">
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
  )

  return (
    <Dialog open={open} onOpenChange={(o) => !o && close()}>
      <DialogContent
        showCloseButton={false}
        className="top-0 left-0 flex h-dvh w-full max-w-none translate-x-0 translate-y-0 flex-col gap-0 rounded-none border-0 bg-card p-0 sm:max-w-none"
        // The title field opens focused, as it did above the form; Radix would pick Cancel, first in the header.
        onOpenAutoFocus={(e) => {
          e.preventDefault()
          document.getElementById('widget-title')?.focus()
        }}
        onEscapeKeyDown={(e) => shouldKeepOpenOnEscape(e.target, document) && e.preventDefault()}
      >
        <form
          className="contents"
          onSubmit={(e) => {
            e.preventDefault()
            void save()
          }}
        >
          <DialogHeader className="h-14 min-w-0 shrink-0 flex-row items-center gap-2 border-b px-4 text-left sm:px-6">
            <DialogTitle className="mr-auto truncate text-base">{widget ? 'Edit widget' : 'Add widget'}</DialogTitle>
            <DialogDescription className="sr-only">Choose a visualization, write its query and save the widget.</DialogDescription>
            <Button type="button" variant="ghost" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" disabled={!canSave}>
              Save
            </Button>
          </DialogHeader>
          {saveError && (
            <Alert variant="destructive" className="shrink-0 rounded-none border-x-0 border-t-0 px-4 sm:px-6">
              <TriangleAlertIcon />
              <AlertDescription className="break-words whitespace-pre-wrap">{saveError}</AlertDescription>
            </Alert>
          )}
          {/*
            Two halves of one shape: a work surface on the dashboard's own
            background above (the widget as it will sit there; the code that
            feeds it), and its details on white below (settings; rows).
            Phone: stacked, scrolling as a whole. From lg: each half scrolls on its own.
          */}
          <div className="grid min-h-0 flex-1 grid-cols-1 overflow-y-auto lg:grid-cols-2 lg:overflow-hidden">
            <div className="flex min-w-0 flex-col lg:min-h-0 lg:overflow-y-auto lg:border-r">
              <div className="shrink-0 border-b bg-background bg-[radial-gradient(var(--border)_1px,transparent_1px)] [background-size:16px_16px] p-4 sm:p-6 lg:p-8">
                {markdown ? (
                  <MarkdownPreview title={draft.title} text={draft.markdown} height={height} />
                ) : (
                  <Preview component={component} props={draft.props} title={draft.title} height={height} state={run} stale={stale} />
                )}
              </div>
              <div className="grid min-w-0 grid-cols-1 content-start gap-y-4 p-4 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-x-4 sm:gap-y-3 sm:p-6">
                <SettingRow label="Title" htmlFor="widget-title">
                  <Input
                    id="widget-title"
                    aria-label="Title"
                    placeholder="Shown on the card"
                    value={draft.title}
                    onChange={(e) => edit({ title: e.target.value })}
                  />
                </SettingRow>
                <SettingRow label="Visualization" htmlFor="widget-component">
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
                  {module && <p className="mt-1.5 text-xs text-muted-foreground">{needsHint(module.contract)}</p>}
                </SettingRow>
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
            </div>
            <div className="flex min-w-0 flex-col border-t lg:min-h-0 lg:border-t-0">
              <div className="flex min-h-12 min-w-0 shrink-0 flex-wrap items-center gap-2 border-b px-4 py-2">
                <span className="mr-auto text-sm font-medium">{markdown ? 'Markdown' : 'SQL'}</span>
                {!markdown && (
                  <>
                    {projectPicker}
                    {uses.range && <RangeSwitcher value={range} timezone={defaults.timezone} onChange={setRange} />}
                    <Button type="button" variant="secondary" size="sm" disabled={!component} onClick={() => void runQuery()}>
                      <PlayIcon />
                      Run query <Kbd className="bg-background/60">{RUN_KEYS}</Kbd>
                    </Button>
                  </>
                )}
              </div>
              <div className={markdown ? 'h-72 min-w-0 lg:h-auto lg:min-h-0 lg:flex-1' : 'h-56 min-w-0 shrink-0 lg:h-[45%]'}>
                <Suspense fallback={<Skeleton className="h-full rounded-none" />}>
                  {markdown ? (
                    <CodeEditor flush language="markdown" label="Markdown" value={draft.markdown} onChange={(md) => edit({ markdown: md })} />
                  ) : (
                    <CodeEditor
                      flush
                      language="sql"
                      label="SQL"
                      value={draft.sql}
                      onChange={(sql) => edit({ sql })}
                      onRun={() => void runQuery()}
                      tables={tables}
                    />
                  )}
                </Suspense>
              </div>
              {!markdown && (
                <>
                  <div className="flex min-h-11 min-w-0 shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-y px-4 py-2">
                    <span className="mr-auto text-sm font-medium">Rows</span>
                    {component && <RunStatus state={run} stale={stale} />}
                  </div>
                  {component ? (
                    <Rows state={run} stale={stale} />
                  ) : (
                    <p className="p-4 text-sm text-muted-foreground">Choose a visualization to run</p>
                  )}
                </>
              )}
            </div>
          </div>
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
function MarkdownPreview({ title, text, height }: { title: string; text: string; height: number }) {
  const Markdown = widgets.markdown.default
  return (
    <section aria-label="Preview" className="min-w-0" style={{ height: rowsPx(height) }}>
      <WidgetFrame title={title}>
        <Suspense fallback={<Skeleton className="h-full" />}>
          <Markdown data={{ markdown: text }} props={{}} />
        </Suspense>
      </WidgetFrame>
    </section>
  )
}
