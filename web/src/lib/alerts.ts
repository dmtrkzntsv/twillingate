// Pure helpers the Alerts tab builds on: values by unit, the threshold input,
// the alert's definition in words, and the rules the server applies to what a
// request may carry (docs/twillingate.md, Alerts).
import type { Alert, AlertCondition, AlertRead, AlertSource, AlertUnit } from './api'

const NONE = '—'
const grouped = new Intl.NumberFormat('en-US', { maximumFractionDigits: 0 })
const twoDecimals = new Intl.NumberFormat('en-US', { maximumFractionDigits: 2 })

/** A 0 to 1 fraction as percent points, rounded so 0.07 reads 7 and not 7.000000000000001. */
function percentPoints(fraction: number): number {
  return Number((fraction * 100).toPrecision(12))
}

/** A value as its unit reads it: `1,240`, `3.4%`, `2.5`, `7h` or `45m` (the bare age of a silence alert); `—` for none. */
export function formatAlertValue(unit: AlertUnit, v: number | null): string {
  if (v === null) return NONE
  switch (unit) {
    case 'fraction':
      return `${percentPoints(v).toFixed(1)}%`
    case 'count':
      return grouped.format(v)
    case 'hours':
      return v < 1 ? `${Math.floor(v * 60)}m` : `${Math.floor(v)}h`
    default:
      return twoDecimals.format(v)
  }
}

/** A threshold as the editor's input shows it: a percent for a fraction, otherwise the number. */
export function thresholdToInput(unit: AlertUnit, value: number): string {
  return String(unit === 'fraction' ? percentPoints(value) : value)
}

/** The threshold an input's text means, in the read's unit; null for empty text or what is not a finite number. */
export function inputToThreshold(unit: AlertUnit, text: string): number | null {
  const t = text.trim()
  if (t === '') return null
  const n = Number(t)
  if (!Number.isFinite(n)) return null
  return unit === 'fraction' ? Number((n / 100).toPrecision(12)) : n
}

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

/** A threshold as words: a fraction's percent with trailing zeros trimmed, otherwise as the unit reads it. */
function thresholdText(unit: AlertUnit, value: number): string {
  if (unit === 'fraction') return `${percentPoints(value)}%`
  return formatAlertValue(unit, value)
}

/** The line an alert watches, in words: `below 3% over 7 days`, `drops 30% against the 7 days before`, `no event for 6 hours`. */
export function describeCondition(a: Pick<Alert, 'condition' | 'unit' | 'window_days'>): string {
  const c = a.condition
  const days = a.window_days
  switch (c.type) {
    case 'threshold':
      return `${c.op} ${thresholdText(a.unit, c.value)} over ${plural(days, 'day')}`
    case 'change': {
      const verb = c.direction === 'drop' ? 'drops' : c.direction === 'rise' ? 'rises' : 'moves'
      const span = days === 1 ? 'day' : `${days} days`
      const against = c.against === 'previous' ? `the ${span} before` : `the same ${span} a week earlier`
      return `${verb} ${c.percent}% against ${against}`
    }
    case 'silence':
      return `no event for ${plural(c.hours, 'hour')}`
  }
}

/** What a template alert watches, from its fields. */
function describeTemplate(template: NonNullable<Alert['template']>, f: Record<string, string>): string {
  const onPath = f.path ? ` on ${f.path}` : ''
  switch (template) {
    case 'event_count':
      return `Count of ${f.event}${onPath}`
    case 'unique_people': {
      const who = f.who === 'users' ? 'Signed-in users' : 'Visitors'
      return f.event ? `${who} doing ${f.event}` : who
    }
    case 'conversion':
      return `Conversion from ${f.from_event} to ${f.to_event}${f.people === 'user' ? ' (signed-in users)' : ''}`
    case 'no_events':
      return `Events named ${f.event}${onPath}`
  }
}

/** What an alert watches, in words. */
export function describeSource(a: Alert): string {
  if (a.template) return describeTemplate(a.template, a.fields ?? {})
  switch (a.kind) {
    case 'funnel':
      return `Funnel step ${a.read.step} over ${a.read.from === 'previous' ? 'the step before' : 'the first step'}`
    case 'stat':
      return `${a.read.metric} of a stat query`
    case 'silence': {
      const n = a.where?.length ?? 0
      return n === 0 ? 'every event' : `Events matching ${plural(n, 'filter')}`
    }
    default:
      return 'A definition that no longer holds'
  }
}

/** The one source a preview or save body carries: the template's, else a silence alert's `where`, else `query` with `read`. list_alerts returns `query` beside the others, and the server refuses two. */
export function alertSource(a: Alert): AlertSource {
  if (a.template) return { template: a.template, fields: a.fields ?? {} }
  if (a.kind === 'silence') return { where: a.where ?? [] }
  return { query: a.query, read: a.read }
}

/** Whether `min_volume` means anything to the server: not for silence, and not for a threshold on a stat with no volume metric (its value is what is watched). */
export function takesMinVolume(kind: string, condition: AlertCondition, read: AlertRead): boolean {
  if (kind === 'funnel') return true
  if (kind === 'stat') return condition.type === 'change' || Boolean(read.volume)
  return false
}

/** The condition types a kind of alert may have. */
export function conditionTypes(kind: string): AlertCondition['type'][] {
  if (kind === 'silence') return ['silence']
  if (kind === 'stat' || kind === 'funnel') return ['threshold', 'change']
  return []
}

/** When a breached alert's current breach began: the oldest evaluation of the newest run of breached in `recent` (newest first). An evaluation with no data, a failed one and a broken one neither end the run nor extend it; an ok one ends it. Null when the alert is not breached. */
export function breachedSince(a: Alert): string | null {
  if (a.state !== 'breached') return null
  let since: string | null = null
  for (const e of a.recent) {
    if (e.outcome === 'breached') since = e.at
    else if (e.outcome === 'ok') break
  }
  return since
}

const SNOOZE_MS = { '1h': 3_600_000, '1d': 86_400_000, '1w': 7 * 86_400_000 }

/** The time a snooze of the choice ends, as an RFC 3339 UTC string without fractions. */
export function snoozeUntil(now: Date, choice: '1h' | '1d' | '1w'): string {
  const end = new Date(now.getTime() + SNOOZE_MS[choice])
  return end.toISOString().replace(/\.\d{3}Z$/, 'Z')
}
