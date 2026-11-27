// The new-alert wizard's rules: the four templates, the blank alert a template
// starts from, the name suggested for it and what a create sends
// (docs/twillingate.md, Alerts: create_alert). Pure, so they are tested apart
// from the dialog; the draft's own rules are alert-draft's.
import { conditionOf, draftErrors, draftOf, previewBody, type AlertDraft, type DraftErrors } from './alert-draft'
import { describeCondition, describeSource } from './alerts'
import type { Alert, AlertCondition, AlertSource } from './api'

type Template = NonNullable<Alert['template']>

/** The longest alert name the server takes. */
const NAME_LIMIT = 100

/** The fewest characters a name cut at a word boundary keeps; a shorter one is cut hard instead. */
const NAME_KEEP = 60

/** The templates a new alert starts from, in the order the wizard offers them. */
export const TEMPLATES: { id: Template; title: string; description: string }[] = [
  { id: 'event_count', title: 'Event count', description: 'How many times an event happens a day, or over a few days.' },
  { id: 'unique_people', title: 'Unique people', description: 'How many visitors or signed-in users do something.' },
  { id: 'conversion', title: 'Conversion', description: 'The share of people who do one event and then another.' },
  { id: 'no_events', title: 'No events', description: 'An event stops arriving for some hours.' },
]

/** The alert a template starts from, for the draft helpers to read: id 0 (no saved alert), the template's kind, unit and read, and its default condition (a threshold below, its value left to the draft). */
export function blankAlert(projectId: number, template: Template): Alert {
  const kind = { event_count: 'stat', unique_people: 'stat', conversion: 'funnel', no_events: 'silence' }[template] as Alert['kind']
  const unit = { event_count: 'count', unique_people: 'count', conversion: 'fraction', no_events: 'hours' }[template] as Alert['unit']
  const read = {
    event_count: { metric: 'count' },
    unique_people: { metric: 'actors' },
    conversion: { step: '2', from: 'start' },
    no_events: {},
  }[template] as Alert['read']
  const condition: AlertCondition = template === 'no_events' ? { type: 'silence', hours: 6 } : { type: 'threshold', op: 'below', value: 0 }
  return {
    id: 0,
    project_id: projectId,
    name: '',
    kind,
    unit,
    query: null,
    read,
    condition,
    window_days: template === 'conversion' ? 7 : 1,
    min_volume: 0,
    template,
    fields: {},
    paused: false,
    snoozed_until: '',
    state: 'pending',
    reason: '',
    last_value: null,
    last_value_text: '',
    last_evaluated_at: '',
    last_day: '',
    check_requested_at: '',
    check_pending: false,
    recent: [],
    created_at: '',
    updated_at: '',
    archived_at: '',
  }
}

/** The draft a blank alert starts as: its inputs from the alert, a threshold's value empty until typed. */
export function blankDraft(blank: Alert): AlertDraft {
  return { ...draftOf(blank), value: '' }
}

/** The blank alert with the draft's fields, as `describeSource` and `takesMinVolume` read it; a unique-people alert reads the metric its `who` names. */
export function wizardAlert(blank: Alert, draft: AlertDraft): Alert {
  const fields = Object.fromEntries(Object.entries(draft.fields).filter(([, v]) => v !== ''))
  const read = blank.template === 'unique_people' ? { ...blank.read, metric: fields.who || 'actors' } : blank.read
  return { ...blank, fields, read }
}

/** A name cut to `NAME_LIMIT` characters (code points, as the server counts): at the last word boundary when that keeps at least `NAME_KEEP` of them, else hard. */
function capName(name: string): string {
  const chars = Array.from(name)
  if (chars.length <= NAME_LIMIT) return name
  const cut = chars.slice(0, NAME_LIMIT + 1)
  const space = cut.lastIndexOf(' ')
  return (space >= NAME_KEEP ? cut.slice(0, space) : cut.slice(0, NAME_LIMIT)).join('').trimEnd()
}

/** The name the wizard offers: what the alert watches and its condition (`Count of signup below 5 over 1 day`), or for no_events `No heartbeat for 6 hours`. */
export function suggestName(blank: Alert, draft: AlertDraft): string {
  const w = wizardAlert(blank, draft)
  const f = w.fields ?? {}
  const condition = conditionOf(blank, draft)
  if (blank.template === 'no_events') {
    const hours = condition.type === 'silence' ? condition.hours : NaN
    return capName(`No ${f.event ?? ''}${f.path ? ` on ${f.path}` : ''} for ${hours} hour${hours === 1 ? '' : 's'}`)
  }
  const window = Number(draft.windowDays)
  return capName(`${describeSource(w)} ${describeCondition({ condition, unit: blank.unit, window_days: window })}`)
}

/** What is wrong with a name the wizard would send, before the server is asked: none (`Enter a name`), or more characters than the server takes (code points, as it counts, once trimmed); undefined when it may be sent. */
export function nameError(name: string): string | undefined {
  const chars = Array.from(name.trim())
  if (chars.length === 0) return 'Enter a name'
  if (chars.length > NAME_LIMIT) return `At most ${NAME_LIMIT} characters`
  return undefined
}

/** create_alert's body for the draft: preview_alert's (so the backtest and the create agree) with the trimmed name. */
export function createBody(
  blank: Alert,
  draft: AlertDraft,
): AlertSource & { name: string; condition: AlertCondition; window_days?: number; min_volume?: number } {
  return { name: draft.name.trim(), ...previewBody(blank, draft) }
}

/** What is wrong with step 2's inputs, by input: the draft's errors but the name's (step 3's); `fields` says a required event is missing. */
export function stepTwoErrors(blank: Alert, draft: AlertDraft): DraftErrors {
  const { name: _name, ...errs } = draftErrors(blank, draft)
  return errs
}
