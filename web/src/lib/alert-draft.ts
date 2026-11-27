// The alert editor's draft: what its inputs hold, as text, and what a save
// or a backtest of it sends (docs/twillingate.md, Alerts: update_alert and
// preview_alert). Pure, so the rules are tested apart from the form.
import { alertSource, conditionTypes, inputToThreshold, takesMinVolume, TEMPLATE_DEFAULTS, thresholdToInput } from './alerts'
import type { Alert, AlertCondition, AlertPatch, AlertSource } from './api'

/** The editor's inputs: every condition type's, so switching type and back keeps what was typed. */
export type AlertDraft = {
  name: string
  conditionType: 'threshold' | 'change' | 'silence'
  op: 'below' | 'above'
  value: string
  direction: 'drop' | 'rise' | 'either'
  percent: string
  against: 'previous' | 'week_ago'
  hours: string
  windowDays: string
  minVolume: string
  fields: Record<string, string>
}

export type DraftErrors = Partial<Record<keyof AlertDraft, string>>

/** The event fields of each template that are required; unique_people's is optional. */
const REQUIRED_EVENTS: Record<NonNullable<Alert['template']>, string[]> = {
  event_count: ['event'],
  unique_people: [],
  conversion: ['from_event', 'to_event'],
  no_events: ['event'],
}

/** The number a text means; NaN for empty text, which `Number` would read as 0. */
function num(text: string): number {
  return text.trim() === '' ? NaN : Number(text)
}

function whole(n: number, min: number, max: number): boolean {
  return Number.isInteger(n) && n >= min && n <= max
}

/** A template's fields as the server takes them: an empty one is an absent one. */
function cleanFields(fields: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(fields).filter(([, v]) => v !== ''))
}

type Template = NonNullable<Alert['template']>

/** A template's fields as the server reads them: empty ones absent, and each absent one that has a default holding it (`who: actors`). */
function effectiveFields(template: Template, fields: Record<string, string>): Record<string, string> {
  return { ...TEMPLATE_DEFAULTS[template], ...cleanFields(fields) }
}

/** Whether two sets of a template's fields mean the same alert, the template's defaults applied to both. */
function sameFields(template: Template, a: Record<string, string>, b: Record<string, string>): boolean {
  const x = effectiveFields(template, a)
  const y = effectiveFields(template, b)
  const keys = Object.keys(x)
  return keys.length === Object.keys(y).length && keys.every((k) => x[k] === y[k])
}

/** The draft's fields as a save or a preview sends them: empty ones dropped, and one that holds its default dropped too, unless the saved alert names it so (left as it was). */
function sentFields(template: Template, fields: Record<string, string>, saved: Record<string, string>): Record<string, string> {
  const out = cleanFields(fields)
  for (const [k, v] of Object.entries(TEMPLATE_DEFAULTS[template] ?? {})) if (out[k] === v && saved[k] !== v) delete out[k]
  return out
}

/** The draft of a saved alert; a condition type it lacks starts from the defaults (below, drops 30% against the days before). */
export function draftOf(a: Alert): AlertDraft {
  const c = a.condition
  const types = conditionTypes(a.kind)
  return {
    name: a.name,
    conditionType: types.includes(c.type) ? c.type : (types[0] ?? 'threshold'),
    op: c.type === 'threshold' ? c.op : 'below',
    value: c.type === 'threshold' ? thresholdToInput(a.unit, c.value) : '',
    direction: c.type === 'change' ? c.direction : 'drop',
    percent: c.type === 'change' ? String(c.percent) : '30',
    against: c.type === 'change' ? c.against : 'previous',
    hours: c.type === 'silence' ? String(c.hours) : '',
    windowDays: String(a.window_days),
    minVolume: String(a.min_volume),
    fields: { ...(a.fields ?? {}) },
  }
}

/** The inputs the editor changed, over a saved alert: each one typed in whole, and a template's fields one by one (only the keys touched). */
export type DraftEdits = Partial<Omit<AlertDraft, 'fields'>> & { fields?: Record<string, string> }

/** The draft of the saved alert with `edits` over it: a template field touched holds what was typed (an emptied one included), every other the saved alert's value, so a refetch bringing another edit of an untouched field shows it and a save sends it back unchanged. */
export function draftWith(a: Alert, edits: DraftEdits | null): AlertDraft {
  const d = draftOf(a)
  if (!edits) return d
  return { ...d, ...edits, fields: { ...d.fields, ...edits.fields } }
}

/** `edits` with `more` over them: an input touched again holds its new value, and a template's fields merge key by key. */
export function addEdits(edits: DraftEdits, more: DraftEdits): DraftEdits {
  const next = { ...edits, ...more }
  if (edits.fields || more.fields) next.fields = { ...edits.fields, ...more.fields }
  return next
}

/** The condition the draft means, in the alert's unit. Only meaningful once `draftErrors` has none for it. */
function conditionOf(a: Alert, d: AlertDraft): AlertCondition {
  switch (d.conditionType) {
    case 'threshold':
      return { type: 'threshold', op: d.op, value: inputToThreshold(a.unit, d.value) ?? NaN }
    case 'change':
      return { type: 'change', direction: d.direction, percent: num(d.percent), against: d.against }
    case 'silence':
      return { type: 'silence', hours: num(d.hours) }
  }
}

/** Whether two numeric inputs differ in what they mean (`3` and `3.0` do not); an input that is no number differs from every other. */
function differs(a: string, b: string): boolean {
  return num(a) !== num(b)
}

/** Whether the draft's condition differs from the saved alert's draft, input by input, each read the same way (so an untouched percent threshold is the same). */
function conditionChanged(saved: AlertDraft, d: AlertDraft): boolean {
  if (d.conditionType !== saved.conditionType) return true
  switch (d.conditionType) {
    case 'threshold':
      return d.op !== saved.op || differs(d.value, saved.value)
    case 'change':
      return d.direction !== saved.direction || differs(d.percent, saved.percent) || d.against !== saved.against
    case 'silence':
      return differs(d.hours, saved.hours)
  }
}

/** Whether the draft's condition reads a window of days (every condition but silence). */
function hasWindow(d: AlertDraft): boolean {
  return d.conditionType !== 'silence'
}

/** Whether the server takes a minimum volume for the draft's condition. */
export function draftTakesMinVolume(a: Alert, d: AlertDraft): boolean {
  return a.kind !== '' && takesMinVolume(a.kind, conditionOf(a, d), a.read)
}

/** What is wrong with the draft, by input; only the inputs the alert shows are judged (a broken alert's name alone). */
export function draftErrors(a: Alert, d: AlertDraft): DraftErrors {
  const errs: DraftErrors = {}
  if (d.name.trim() === '') errs.name = 'Enter a name'
  if (a.kind === '') return errs
  switch (d.conditionType) {
    case 'threshold':
      if (inputToThreshold(a.unit, d.value) === null) errs.value = 'Enter a number'
      break
    case 'change': {
      const p = num(d.percent)
      if (!(p > 0 && p <= 1000)) errs.percent = 'Enter a percent above 0, at most 1000'
      break
    }
    case 'silence':
      if (!whole(num(d.hours), 1, 720)) errs.hours = 'Enter whole hours from 1 to 720'
      break
  }
  if (hasWindow(d) && !whole(num(d.windowDays), 1, 30)) errs.windowDays = 'Enter whole days from 1 to 30'
  if (draftTakesMinVolume(a, d)) {
    const v = num(d.minVolume)
    if (!(v === 0 || v >= 1)) errs.minVolume = '0 for none, or at least 1'
  }
  if (a.template && REQUIRED_EVENTS[a.template].some((k) => !d.fields[k])) errs.fields = 'Choose an event'
  return errs
}

/** update_alert's body for the draft: only what changed; the condition whole when any part did, the fields whole when any did. */
export function patchOf(a: Alert, d: AlertDraft): AlertPatch {
  const patch: AlertPatch = {}
  const name = d.name.trim()
  if (name !== a.name) patch.name = name
  if (a.kind === '') return patch
  const saved = draftOf(a)
  if (conditionChanged(saved, d)) patch.condition = conditionOf(a, d)
  if (hasWindow(d) && differs(d.windowDays, saved.windowDays)) patch.window_days = num(d.windowDays)
  if (draftTakesMinVolume(a, d)) {
    if (differs(d.minVolume, saved.minVolume)) patch.min_volume = num(d.minVolume)
  } else if (a.min_volume !== 0) {
    // A condition that takes none (a stat's change turned threshold, with no volume read): the server refuses the one kept.
    patch.min_volume = 0
  }
  if (a.template && !sameFields(a.template, d.fields, a.fields ?? {})) patch.fields = sentFields(a.template, d.fields, a.fields ?? {})
  return patch
}

/** Whether the draft changes what the alert watches or when it fires (anything a save sends but the name). */
export function definitionChanged(a: Alert, d: AlertDraft): boolean {
  return Object.keys(patchOf(a, d)).some((k) => k !== 'name')
}

/** preview_alert's body for the draft: the alert's one source (a template's with the draft's fields), the draft's condition, its window and, where taken, its minimum volume. */
export function previewBody(a: Alert, d: AlertDraft): AlertSource & { condition: AlertCondition; window_days?: number; min_volume?: number } {
  const body: AlertSource & { condition: AlertCondition; window_days?: number; min_volume?: number } = a.template
    ? { template: a.template, fields: sentFields(a.template, d.fields, a.fields ?? {}), condition: conditionOf(a, d) }
    : { ...alertSource(a), condition: conditionOf(a, d) }
  if (hasWindow(d)) body.window_days = num(d.windowDays)
  if (draftTakesMinVolume(a, d)) body.min_volume = num(d.minVolume)
  return body
}
