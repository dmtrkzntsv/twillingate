import { lazy, Suspense, useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import { useQuery } from '@tanstack/react-query'
import { BracesIcon, PlayIcon, TriangleAlertIcon } from 'lucide-react'
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
import { carryProps, fieldsOf, needsHint } from '@/lib/props-form'
import { parseSchemaViews } from '@/lib/schema-views'
import { codeTypesOf, draftOf, isDirty, paramsOf, sourceOf, sourceTypeOf, withCode, withComponent, type CodeType, type Draft } from '@/lib/widget-draft'
import { iconOf } from './component-icons'
import PropsForm, { SettingRow } from './PropsForm'
import Splitter from './Splitter'
import { Preview, problemOf, ranOf, Rows, RUN_KEYS, RunStatus, type RunState } from './ResultsPane'

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

/** The work surfaces' padding around the widget card, from lg (Tailwind's p-8). */
const SURFACE_PAD_PX = 32

// The heights of a toolbar and a bar (lg:h-12, lg:h-11), and the least the details keep.
const TOOLBAR_PX = 48
const BAR_PX = 44
const DETAILS_MIN_PX = 96
// The SQL editor's own least height (CodeEditor's min-h-40).
const SURFACE_MIN_PX = 160
// Neither half narrower than this share of the width.
const SPLIT_MIN = 25
const SPLIT_KEY = 'twillingate.editor.split'

function readSplit(): number {
  try {
    const v = Number(localStorage.getItem(SPLIT_KEY))
    return v >= SPLIT_MIN && v <= 100 - SPLIT_MIN ? v : 50
  } catch {
    return 50
  }
}

/**
 * An element's size, kept as it changes. A callback ref, not an object:
 * the dialog's content mounts a render after the editor does.
 */
function useSize(): [(el: HTMLElement | null) => void, { width: number; height: number }] {
  const [el, setEl] = useState<HTMLElement | null>(null)
  const [size, setSize] = useState({ width: 0, height: 0 })
  useEffect(() => {
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(([entry]) => setSize({ width: entry.contentRect.width, height: entry.contentRect.height }))
    ro.observe(el)
    return () => ro.disconnect()
  }, [el])
  return [setEl, size]
}

// The two halves' header rows: one height each, so their lines meet across the divide.
const toolbar = 'flex min-h-12 min-w-0 shrink-0 flex-wrap items-center gap-2 border-b px-4 py-2 sm:px-6 lg:h-12 lg:flex-nowrap lg:py-0'
const bar = 'flex min-h-11 min-w-0 shrink-0 flex-wrap items-center gap-x-3 gap-y-1 border-b px-4 py-2 sm:px-6 lg:h-11 lg:flex-nowrap lg:py-0'

/** How long a change of visualization, props, project or range waits before it runs the query. */
const AUTO_RUN_MS = 400

const codeLabel: Record<CodeType, string> = { sql: 'SQL', query: 'Query' }

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
  const [raw, setRaw] = useState(false)
  // The panes' sizes from lg: the halves' split (a percentage, kept on this device) and the surfaces' height.
  const [split, setSplit] = useState(readSplit)
  const keepSplit = (v: number) => {
    setSplit(v)
    try {
      localStorage.setItem(SPLIT_KEY, String(Math.round(v * 10) / 10))
    } catch {
      // Storage blocked: the split lasts until the editor closes.
    }
  }
  const [surfacePx, setSurfacePx] = useState<number | null>(null)
  const [box, size] = useSize()
  const [saving, setSaving] = useState(false)
  const savingNow = useRef(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [confirmDiscard, setConfirmDiscard] = useState(false)

  const component = known(draft.component) ? draft.component : null
  const module = component ? widgets[component] : undefined
  const type = sourceTypeOf({ ...draft, component })
  const markdown = type === 'md'
  const codes = codeTypesOf(component, draft.props)
  // The code the run reads: the query's JSON or the SQL.
  const code = type === 'query' ? draft.query : draft.sql
  const codeType: CodeType = type === 'query' ? 'query' : 'sql'
  const uses = paramsOf({ ...draft, component })
  // The dashboard hands over its range resolved; a preset picked here is resolved now.
  const span = range.from && range.to ? { from: range.from, to: range.to } : resolve(range.range, defaults.timezone, new Date())
  // What a run reads: a change to any of it after the run dims its rows.
  const runKey = JSON.stringify([codeType, code, uses.project ? projectId : null, uses.range ? span : null])
  const stale = ranWith !== null && ranWith.key !== runKey
  const last = ranOf(run)?.result
  // What an automatic run reads; null while there is nothing it could run.
  const autoKey =
    component && !markdown && code.trim() !== '' && (!uses.project || projectId !== undefined)
      ? JSON.stringify([component, codeType, draft.props, uses.project ? projectId : null, uses.range ? span : null])
      : null
  const ranAuto = useRef<string | null>(null)
  const everRan = ranWith !== null
  const editing = widget !== undefined
  // An automatic run never runs SQL typed since the last run: that waits for ⌘↵.
  const autoDue = autoKey !== null && autoKey !== ranAuto.current && (everRan ? ranWith.sql === code : editing)

  const runQuery = async (auto = false) => {
    if (!component || markdown) return
    // Checked again as it fires: a run since it was scheduled may have made it moot.
    if (auto && !autoDue) return
    const n = ++latest.current
    ranAuto.current = autoKey
    setRanWith({ key: runKey, sql: code })
    setRun((r) => ({ kind: 'running', last: ranOf(r) }))
    const props = draft.props
    try {
      const result = await endpoints.previewWidget({
        component,
        props,
        source: { type: codeType, content: code },
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
  // The surfaces fit the card until dragged; the room left keeps a bar and some details below.
  const fit = rowsPx(height) + 2 * SURFACE_PAD_PX
  const surfaceMax = Math.max(SURFACE_MIN_PX, size.height - TOOLBAR_PX - BAR_PX - DETAILS_MIN_PX)
  const surface = Math.min(surfacePx ?? fit, surfaceMax > SURFACE_MIN_PX ? surfaceMax : Infinity)
  const hasFields = module !== undefined && fieldsOf(module.contract.props).length > 0
  const problem = markdown ? undefined : problemOf(run, component, draft.props)

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
            Two halves of one shape, row for row: a toolbar, a work surface
            on the dashboard's own background (the widget as it will sit
            there; the code that feeds it), a bar, then the details on white
            (settings; rows). The surfaces share one height, so every line
            runs across both. Phone: stacked, scrolling as a whole.
          */}
          <div
            ref={box}
            className="relative grid min-h-0 flex-1 grid-cols-1 overflow-y-auto lg:grid-cols-[var(--left)_minmax(0,1fr)] lg:overflow-hidden"
            style={{ '--left': `${split}%`, '--surface': `${surface}px` } as CSSProperties}
          >
            <Splitter
              orientation="vertical"
              label="Resize the halves"
              value={split}
              min={SPLIT_MIN}
              max={100 - SPLIT_MIN}
              step={2}
              perPx={size.width ? 100 / size.width : 0}
              onChange={keepSplit}
              onReset={() => keepSplit(50)}
              style={{ left: `${split}%`, top: 0, bottom: 0 }}
              className="max-lg:hidden"
            />
            <Splitter
              orientation="horizontal"
              label="Resize the work surfaces"
              value={surface}
              min={SURFACE_MIN_PX}
              max={surfaceMax}
              step={16}
              perPx={1}
              onChange={setSurfacePx}
              onReset={() => setSurfacePx(null)}
              style={{ top: TOOLBAR_PX + surface, left: 0, width: markdown ? `${split}%` : '100%' }}
              className="max-lg:hidden"
            />
            <div className="flex min-w-0 flex-col lg:min-h-0 lg:border-r">
              <div className={toolbar}>
                <span className="mr-auto text-sm font-medium">Preview</span>
                <Select
                  value={component ?? ''}
                  onValueChange={(next) =>
                    setDraft((d) => withComponent(d, next, carryProps(d.props, widgets[next].contract.props)))
                  }
                >
                  <SelectTrigger size="sm" aria-label="Visualization" className="w-48 min-w-0">
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
              </div>
              <div className="shrink-0 overflow-auto border-b bg-background bg-[radial-gradient(var(--border)_1px,transparent_1px)] [background-size:16px_16px] p-4 sm:p-6 lg:h-[var(--surface)] lg:p-8">
                {markdown ? (
                  <MarkdownPreview title={draft.title} text={draft.markdown} height={height} />
                ) : (
                  <Preview component={component} props={draft.props} title={draft.title} height={height} state={run} stale={stale} />
                )}
              </div>
              <div className={bar}>
                <span className="mr-auto text-sm font-medium">Settings</span>
                {hasFields && (
                  <Button type="button" variant="ghost" size="sm" className="-mr-2 h-7 text-muted-foreground" onClick={() => setRaw(!raw)}>
                    <BracesIcon />
                    {raw ? 'Edit props as a form' : 'Edit props as JSON'}
                  </Button>
                )}
              </div>
              <div className="min-w-0 lg:min-h-0 lg:flex-1 lg:overflow-y-auto">
                {problem && (
                  <Alert className="rounded-none border-x-0 border-t-0 px-4 sm:px-6">
                    <TriangleAlertIcon />
                    <AlertDescription className="break-words">{problem}</AlertDescription>
                  </Alert>
                )}
                <div className="grid min-w-0 grid-cols-1 content-start gap-y-4 p-4 sm:grid-cols-[8rem_minmax(0,1fr)] sm:gap-x-4 sm:gap-y-3 sm:px-6">
                  <SettingRow label="Title" htmlFor="widget-title">
                    <Input
                      id="widget-title"
                      aria-label="Title"
                      placeholder="Shown on the card"
                      value={draft.title}
                      onChange={(e) => edit({ title: e.target.value })}
                    />
                  </SettingRow>
                  <SettingRow label={markdown ? 'Source' : 'Columns'}>
                    <p className="flex min-h-9 items-center text-sm text-muted-foreground">
                      {module ? needsHint(module.contract).replace(/^needs /, '') : 'Choose a visualization above the preview'}
                    </p>
                  </SettingRow>
                  {component && (
                    <PropsForm
                      component={component}
                      value={draft.props}
                      onChange={(props) => edit({ props })}
                      columns={last?.columns ?? []}
                      onValidity={setPropsValid}
                      raw={raw && hasFields}
                    />
                  )}
                </div>
              </div>
            </div>
            <div className="flex min-w-0 flex-col border-t lg:min-h-0 lg:border-t-0">
              <div className={toolbar}>
                {codes.length > 1 ? (
                  <div role="group" aria-label="Source type" className="mr-auto flex items-center gap-1">
                    {codes.map((c) => (
                      <Button
                        key={c}
                        type="button"
                        size="sm"
                        variant={c === codeType ? 'secondary' : 'ghost'}
                        aria-pressed={c === codeType}
                        className="h-7"
                        onClick={() => setDraft((d) => withCode(d, c))}
                      >
                        {codeLabel[c]}
                      </Button>
                    ))}
                  </div>
                ) : (
                  <span className="mr-auto text-sm font-medium">{markdown ? 'Markdown' : codeLabel[codeType]}</span>
                )}
                {draft.code === 'query' && type === 'sql' && (
                  <span className="text-xs text-muted-foreground">A remote table takes SQL only</span>
                )}
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
              <div className={markdown ? 'h-72 min-w-0 lg:h-auto lg:min-h-0 lg:flex-1' : 'h-56 min-w-0 shrink-0 border-b lg:h-[var(--surface)]'}>
                <Suspense fallback={<Skeleton className="h-full rounded-none" />}>
                  {markdown ? (
                    <CodeEditor flush language="markdown" label="Markdown" value={draft.markdown} onChange={(md) => edit({ markdown: md })} />
                  ) : (
                    <CodeEditor
                      key={codeType}
                      flush
                      language={codeType === 'query' ? 'json' : 'sql'}
                      label={codeLabel[codeType]}
                      value={code}
                      onChange={(text) => edit(codeType === 'query' ? { query: text } : { sql: text })}
                      onRun={() => void runQuery()}
                      tables={codeType === 'sql' ? tables : undefined}
                    />
                  )}
                </Suspense>
              </div>
              {!markdown && (
                <>
                  <div className={bar}>
                    <span className="mr-auto text-sm font-medium">Rows</span>
                    {component && <RunStatus state={run} stale={stale} />}
                  </div>
                  {component ? (
                    <Rows state={run} stale={stale} />
                  ) : (
                    <p className="px-4 py-4 text-sm text-muted-foreground sm:px-6">Choose a visualization to run</p>
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
