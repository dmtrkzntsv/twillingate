import type { Attribution, Form, ProjectActivity } from './api'

const DAY = 86_400_000

/** Whether a project's forms have something to look at: submissions not read yet, or a draft waiting to be approved. */
export function formsNeedAttention(a: ProjectActivity | undefined): boolean {
  return a !== undefined && (a.new_submissions > 0 || a.draft_forms > 0)
}

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
export function formatSource(a: Attribution): string {
  const utm = [a.utm_source, a.utm_medium, a.utm_campaign].filter(Boolean).join(' / ')
  return [utm, a.referrer].filter(Boolean).join(' · ')
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
 * table's column order), then every other field seen, then the ones
 * checked that nothing sent yet (added by name, not saved yet).
 */
export function pickerFields(fields: string[], kept: string[] = [], checked: string[] = []): string[] {
  const listed = [...kept, ...fields.filter((f) => !kept.includes(f))]
  return [...listed, ...checked.filter((f) => !listed.includes(f))]
}

const MAX_FIELD_NAME = 64

/**
 * Why `name` cannot be a field added by name, as the server would refuse
 * it: a `$` prefix (the context keys), more than 64 characters, or a
 * control character. `''` when it can.
 */
export function fieldNameProblem(name: string): string {
  if (name.startsWith('$')) return 'Names starting with $ are reserved for context keys.'
  if ([...name].length > MAX_FIELD_NAME) return `At most ${MAX_FIELD_NAME} characters.`
  // eslint-disable-next-line no-control-regex
  if (/[\u0000-\u001f\u007f]/.test(name)) return 'No control characters.'
  return ''
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
