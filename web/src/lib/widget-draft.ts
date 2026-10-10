import { widgets } from '@/components/widgets'
import type { Widget, WidgetBody, WidgetContentBody, WidgetSource } from './api'

/** What the editor holds while a widget is added or edited: sql and markdown are kept apart, so switching component and back loses neither. */
export interface Draft {
  title: string
  component: string | null
  props: Record<string, unknown>
  sql: string
  markdown: string
}

/** The draft an edit starts from: the widget as stored, or empty for a new one. */
export function draftOf(widget?: Widget): Draft {
  if (!widget) return { title: '', component: null, props: {}, sql: '', markdown: '' }
  const md = widget.source.type === 'md'
  return {
    title: widget.title ?? '',
    component: widget.component,
    props: widget.props ?? {},
    sql: md ? '' : widget.source.content,
    markdown: md ? widget.source.content : '',
  }
}

/** The source type a component takes: every component accepts exactly one today. */
export function sourceTypeOf(component: string | null): 'sql' | 'md' | null {
  if (!component) return null
  return widgets[component]?.contract.accepts[0] ?? null
}

/** The draft's source for its component's type; null while no component is chosen. */
export function sourceOf(d: Draft): WidgetSource | null {
  const type = sourceTypeOf(d.component)
  if (!type) return null
  return { type, content: type === 'md' ? d.markdown : d.sql }
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
  if (!w) return !same(d, draftOf())
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
