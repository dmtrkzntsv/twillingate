import type { Form, Submission } from './api'

const DAY = 86_400_000

/** A day as the forms pages name it, in the viewer's time: "Oct 9". */
export function formatDay(d: Date): string {
  return new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric' }).format(d)
}

/** A day and time, in the viewer's time: "Oct 9, 5:00 PM". */
export function formatDayTime(d: Date): string {
  return new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' }).format(d)
}

/**
 * Where a submission's visit came from, on one line: its UTM source,
 * medium and campaign as "news / email / fall", then the referrer after a
 * dot; the parts it has, "" with none.
 */
export function formatSource(s: Pick<Submission, 'referrer' | 'utm_source' | 'utm_medium' | 'utm_campaign'>): string {
  const utm = [s.utm_source, s.utm_medium, s.utm_campaign].filter((v) => v !== '').join(' / ')
  return [utm, s.referrer].filter((v) => v !== '').join(' · ')
}

/** A time as the API takes it: RFC 3339 in UTC, whole seconds. */
export function rfc3339(d: Date): string {
  return d.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

/** Whether the form refuses submissions from its closing time on, as of `now`. */
export function isClosed(form: Pick<Form, 'closes_at'>, now = Date.now()): boolean {
  return form.closes_at !== undefined && Date.parse(form.closes_at) <= now
}

/**
 * A form's status line (D12): `Closed` once its closing time has passed,
 * else a draft's `Draft · expires in N days` (N rounded up; "today" under a
 * day), else `Closes <date>` with a closing time ahead, else `Approved`.
 */
export function formStatus(form: Form, now = Date.now()): string {
  if (isClosed(form, now)) return 'Closed'
  if (form.status === 'draft') {
    const left = form.draft_until ? Date.parse(form.draft_until) - now : 0
    if (left < DAY) return 'Draft · expires today'
    const n = Math.ceil(left / DAY)
    return `Draft · expires in ${n} ${n === 1 ? 'day' : 'days'}`
  }
  if (form.closes_at) return `Closes ${formatDay(new Date(form.closes_at))}`
  return 'Approved'
}

/**
 * The fields a picker offers, in order: the kept ones in their order (the
 * table's column order), then every other field seen.
 */
export function pickerFields(fields: string[], kept: string[] = []): string[] {
  return [...kept, ...fields.filter((f) => !kept.includes(f))]
}

/**
 * `checked` with `field` toggled: unchecking keeps the others' order, and
 * checking appends, so the columns already chosen do not move.
 */
export function toggleField(checked: string[], field: string): string[] {
  return checked.includes(field) ? checked.filter((f) => f !== field) : [...checked, field]
}

/** Where a form lives under a project, as a link from the Forms tab. */
export function formPath(projectId: number, name: string, search = ''): string {
  const path = `/projects/${projectId}/forms/${encodeURIComponent(name)}`
  return search ? `${path}?${search}` : path
}
