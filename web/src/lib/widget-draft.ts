import { widgets } from '@/components/widgets'
import type { Widget, WidgetBody, WidgetContentBody, WidgetSource } from './api'

/** The two code source types a component may take; markdown is the third, and the only one its component takes. */
export type CodeType = 'sql' | 'query'

/**
 * What the editor holds while a widget is added or edited: sql, query and
 * markdown are kept apart, so switching component or code type and back
 * loses none of them. `code` is the code type chosen, for a component that
 * takes both.
 */
export interface Draft {
  title: string
  component: string | null
  props: Record<string, unknown>
  code: CodeType
  sql: string
  query: string
  markdown: string
}

/** The draft an edit starts from: the widget as stored, or empty for a new one (on sql, its components' first type). */
export function draftOf(widget?: Widget): Draft {
  if (!widget) return { title: '', component: null, props: {}, code: 'sql', sql: '', query: '', markdown: '' }
  const { type, content } = widget.source
  return {
    title: widget.title ?? '',
    component: widget.component,
    props: widget.props ?? {},
    code: type === 'query' ? 'query' : 'sql',
    sql: type === 'sql' ? content : '',
    query: type === 'query' ? content : '',
    markdown: type === 'md' ? content : '',
  }
}

/** The code types a component takes with these props, in its own order; a remote table runs its SQL per request, so it takes sql only. */
export function codeTypesOf(component: string | null, props: Record<string, unknown>): CodeType[] {
  if (!component) return []
  const accepts = widgets[component]?.contract.accepts ?? []
  return accepts.filter((t): t is CodeType => (t === 'sql' || t === 'query') && !(t === 'query' && component === 'table' && props.mode === 'remote'))
}

/** The source type the draft writes: its chosen code type when the component takes it, otherwise the component's first. */
export function sourceTypeOf(d: Pick<Draft, 'component' | 'props' | 'code'>): WidgetSource['type'] | null {
  if (!d.component) return null
  const codes = codeTypesOf(d.component, d.props)
  if (codes.includes(d.code)) return d.code
  return widgets[d.component]?.contract.accepts[0] ?? null
}

/** The draft's source for its type; null while no component is chosen. */
export function sourceOf(d: Draft): WidgetSource | null {
  const type = sourceTypeOf(d)
  if (!type) return null
  return { type, content: type === 'md' ? d.markdown : type === 'query' ? d.query : d.sql }
}

/** The draft on another code type; an empty query starts from the component's template. */
export function withCode(d: Draft, code: CodeType): Draft {
  if (code === 'query' && d.query.trim() === '') return { ...d, code, query: queryTemplate(d.component) }
  return { ...d, code }
}

/**
 * The draft on another component, with these props: a query still on the
 * old component's template moves to the new one's; an edited one stays.
 */
export function withComponent(d: Draft, component: string, props: Record<string, unknown>): Draft {
  const untouched = d.query === queryTemplate(d.component)
  return { ...d, component, props, query: untouched && d.query !== '' ? queryTemplate(component) : d.query }
}

// The breakdown dimension's input name, for the components that read one text column and a value.
const labelInput: Record<string, string> = { bar_list: 'label', pie: 'label', radial: 'label', treemap: 'label', funnel: 'step', radar: 'axis' }

/**
 * A small structured query a new query source starts from, renamed to the
 * component's inputs where it has fixed ones: a view count for a stat, views
 * per day for the charts over time, views by path for the lists, and views
 * by path otherwise. Every one fits its component's inputs (a test checks).
 */
export function queryTemplate(component: string | null): string {
  const metrics = [{ op: 'count' }]
  let content: Record<string, unknown>
  if (component === 'stat') {
    content = { query: { kind: 'stat', table: 'views', metrics }, rename: { count: 'value' } }
  } else if (component === 'line' || component === 'area' || component === 'bar') {
    content = { query: { kind: 'trend', table: 'views', metrics }, rename: { day: 'x', count: 'y' } }
  } else if (component === 'calendar') {
    content = { query: { kind: 'trend', table: 'views', metrics }, rename: { count: 'value' } }
  } else if (component === 'combo') {
    // A trend takes one metric: the combo's two are a breakdown by day.
    const query = { kind: 'breakdown', table: 'views', metrics: [{ op: 'count' }, { op: 'actors' }], dimensions: ['day'], limit: 100 }
    content = { query, rename: { day: 'x', count: 'bar', actors: 'line' } }
  } else if (component === 'heatmap') {
    content = {
      query: { kind: 'breakdown', table: 'views', metrics, dimensions: ['day', 'device'], limit: 100 },
      rename: { day: 'x', device: 'y', count: 'value' },
    }
  } else if (component === 'sankey') {
    content = {
      query: { kind: 'breakdown', table: 'views', metrics, dimensions: ['referrer_source', 'path'], limit: 20 },
      rename: { referrer_source: 'source', path: 'target', count: 'value' },
    }
  } else if (component === 'scatter') {
    content = {
      query: { kind: 'breakdown', table: 'views', metrics: [{ op: 'count' }, { op: 'actors' }], dimensions: ['path'], limit: 20 },
      rename: { path: 'series', count: 'x', actors: 'y' },
    }
  } else if (component === 'map') {
    content = { query: { kind: 'breakdown', table: 'views', metrics, dimensions: ['country'], limit: 10 }, rename: { count: 'value' } }
  } else {
    const query = { kind: 'breakdown', table: 'views', metrics, dimensions: ['path'], limit: 10 }
    const label = component ? labelInput[component] : undefined
    content = label ? { query, rename: { path: label, count: 'value' } } : { query }
  }
  return JSON.stringify(content, null, 2)
}

/** The add_widget body for a draft; throws when no component is chosen. */
export function addBody(d: Draft): WidgetBody {
  const source = sourceOf(d)
  if (!d.component || !source) throw new Error('no component chosen')
  const title = d.title.trim()
  return { component: d.component, ...(title ? { title } : {}), props: d.props, source }
}

// Objects with their keys sorted, at every depth: the server answers props
// with sorted keys, the form keeps the order it was edited in.
const sorted = (_key: string, v: unknown) =>
  typeof v === 'object' && v !== null && !Array.isArray(v)
    ? Object.fromEntries(Object.entries(v).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)))
    : v

const same = (a: unknown, b: unknown) => JSON.stringify(a, sorted) === JSON.stringify(b, sorted)

/** The update_widget body for what the draft changed, and nothing else (spec D14). */
export function changedFields(w: Widget, d: Draft): WidgetContentBody {
  const out: WidgetContentBody = {}
  const title = d.title.trim()
  if (title !== (w.title ?? '')) out.title = title
  if (d.component && d.component !== w.component) out.component = d.component
  if (!same(d.props, w.props ?? {})) out.props = d.props
  const source = sourceOf(d)
  if (source && (source.type !== w.source.type || source.content !== w.source.content)) out.source = source
  return out
}

/** Whether closing the editor would lose something: an edit differs from the widget, an add from the empty draft. */
export function isDirty(w: Widget | undefined, d: Draft): boolean {
  // A template no one edited is not a change, once the draft is back on sql.
  if (!w) return !same({ ...d, query: d.query === queryTemplate(d.component) ? '' : d.query }, draftOf())
  return Object.keys(changedFields(w, d)).length > 0
}

/**
 * Which switcher parameters the SQL reads, with string literals, quoted
 * names and comments blanked first; the server binds exactly what
 * readsql.Check finds, so a miss here only costs its 400.
 */
export function usesParams(sql: string): { project: boolean; range: boolean } {
  const code = sql.replace(/'(?:[^']|'')*'|"(?:[^"]|"")*"|`[^`]*`|\[[^\]]*\]|--[^\n]*|\/\*[\s\S]*?(?:\*\/|$)/g, ' ')
  return { project: /:project\b/.test(code), range: /:(?:from|to)\b/.test(code) }
}

/** Which switcher parameters the draft's source reads: a query always follows both, markdown neither. */
export function paramsOf(d: Draft): { project: boolean; range: boolean } {
  const type = sourceTypeOf(d)
  if (type === 'query') return { project: true, range: true }
  if (type === 'sql') return usesParams(d.sql)
  return { project: false, range: false }
}
