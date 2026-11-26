import type { Widget } from './api'

/**
 * The values picked of each of a dashboard's selectors, by name. An absent
 * name or an empty list is All. Not the project and range a page shows
 * (`Selection` in selection.ts), which this rides beside in the URL.
 */
export type Selection = Record<string, string[]>

/** The URL parameter of a selector: `s.<name>`, repeated once per value, so a comma in a value needs no escaping. */
const PREFIX = 's.'

/**
 * The selection the URL names for a dashboard whose selectors are `names`:
 * each `s.<name>` parameter's values, in order. A selector with no
 * parameter is left out (All); a parameter for a name the dashboard does
 * not declare is dropped.
 */
export function readSelection(params: URLSearchParams, names: string[]): Selection {
  const sel: Selection = {}
  for (const name of names) {
    const values = params.getAll(PREFIX + name)
    if (values.length > 0) sel[name] = values
  }
  return sel
}

/**
 * A copy of `params` with its selection replaced by `sel`: every `s.`
 * parameter goes, then each picked value is appended. The project, the
 * range and anything else stay as they are.
 */
export function writeSelection(params: URLSearchParams, sel: Selection): URLSearchParams {
  const out = new URLSearchParams()
  for (const [key, value] of params) if (!key.startsWith(PREFIX)) out.append(key, value)
  for (const [name, values] of Object.entries(sel)) for (const v of values) out.append(PREFIX + name, v)
  return out
}

/**
 * What a widget is sent of the selection: the selectors it follows that
 * have values, in the dashboard's order (`order`, its selector names), or
 * undefined when none has. A widget is never sent a selector it does not
 * follow, so picking one reloads only the widgets that narrow by it.
 */
export function widgetSelectors(widget: Pick<Widget, 'follows_selectors'>, sel: Selection, order: string[]): Selection | undefined {
  const out: Selection = {}
  for (const name of order) {
    const values = sel[name]
    if (values?.length && widget.follows_selectors.includes(name)) out[name] = values
  }
  return Object.keys(out).length > 0 ? out : undefined
}

/** How a value reads in a selector's button and checklist: the empty string, which a URL can carry, as "(empty)". */
export function valueLabel(value: string): string {
  return value === '' ? '(empty)' : value
}

/** A selector's button: "Deployment: All", "Deployment: cloud", or "Deployment: 2 selected". */
export function selectionLabel(label: string, values: string[] | undefined): string {
  if (!values?.length) return `${label}: All`
  if (values.length === 1) return `${label}: ${valueLabel(values[0])}`
  return `${label}: ${values.length} selected`
}

/** The most projects one selector values request reads (the route refuses more). */
export const VALUE_PROJECTS_MAX = 10

/** The most values the server takes picked for one selector. */
export const PICKS_MAX = 100

/**
 * The projects a selector's values are read from: the project the page
 * shows (the switcher's, or a project page's), then each project a query
 * widget fixes as its `project_id`; or, when that leaves none, every live
 * project, the newest first (ids ascend with creation). At most VALUE_PROJECTS_MAX, each once.
 */
export function selectorProjects(widgets: readonly Pick<Widget, 'source'>[], shown: number | undefined, live: number[]): number[] {
  const ids = new Set<number>()
  if (shown !== undefined) ids.add(shown)
  for (const { source } of widgets) {
    if (source.type !== 'query') continue
    try {
      const id = (JSON.parse(source.content) as { project_id?: unknown } | null)?.project_id
      if (typeof id === 'number' && id > 0) ids.add(id)
    } catch {
      // A query that does not parse fixes no project.
    }
  }
  return [...(ids.size > 0 ? ids : [...live].sort((a, b) => b - a))].slice(0, VALUE_PROJECTS_MAX)
}
