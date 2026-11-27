import { useId } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { TEMPLATE_DEFAULTS } from '@/lib/alerts'
import type { Alert } from '@/lib/api'
import { dashboardsQuery, eventNamesQuery } from '@/lib/queries'
import { resolve } from '@/lib/ranges'

/** The event a page view is stored under: the only one a path narrows in event_count and no_events (a conversion step's path narrows any event). The event list leaves out `$` names, so it is offered on its own. */
const PAGE_VIEW = '$page_view'

type Template = NonNullable<Alert['template']>

/** One event field of a template, and the path field that narrows it: with `viewsOnly`, only while the event is a page view (event_count, no_events); otherwise any event (a conversion step, whose events carry their last view's path). */
interface EventField {
  key: string
  label: string
  path?: { key: string; label: string; viewsOnly: boolean }
  /** The empty choice's label, when the event may be left empty. */
  every?: string
}

const EVENT_FIELDS: Record<Template, EventField[]> = {
  event_count: [{ key: 'event', label: 'Event', path: { key: 'path', label: 'Page path', viewsOnly: true } }],
  unique_people: [{ key: 'event', label: 'Event', every: 'Every event' }],
  conversion: [
    { key: 'from_event', label: 'From event', path: { key: 'from_path', label: 'From page path', viewsOnly: false } },
    { key: 'to_event', label: 'To event', path: { key: 'to_path', label: 'To page path', viewsOnly: false } },
  ],
  no_events: [{ key: 'event', label: 'Event', path: { key: 'path', label: 'Page path', viewsOnly: true } }],
}

/** The people choice of a template: its field, label, and values with their names. */
const PEOPLE_FIELDS: Partial<Record<Template, { key: string; label: string; choices: [string, string][] }>> = {
  unique_people: { key: 'who', label: 'Who', choices: [['actors', 'Visitors'], ['users', 'Signed-in users']] },
  conversion: { key: 'people', label: 'People', choices: [['actor', 'Visitors'], ['user', 'Signed-in users']] },
}

interface Props {
  projectId: number
  template: Template
  fields: Record<string, string>
  /** The fields changed, by key; the others are left out, so the page keeps following the saved alert's. */
  onChange: (changed: Record<string, string>) => void
  /** Shown under the fields. */
  error?: string
}

/**
 * A template's fields (docs/twillingate.md, Templates): an event picker
 * for each event field, over the names the project sent in the last 30
 * days, with `$page_view` first and the field's own value kept even when the
 * list lacks it (an event no longer sent); a path under an event: for
 * event_count and no_events only while it is `$page_view`, cleared when the
 * event changes away from it, and under both of a conversion's events
 * always, kept across a change of event; and the people a template counts,
 * its default shown when the field is absent. An empty field is an absent
 * one.
 */
export default function TemplateFields({ projectId, template, fields, onChange, error }: Props) {
  const id = useId()
  const tz = useQuery(dashboardsQuery).data?.timezone ?? 'UTC'
  const names = useQuery(eventNamesQuery(projectId, resolve('30d', tz)))
  const listed = [...new Set((names.data?.events ?? []).map((e) => e.name))].filter((n) => n !== PAGE_VIEW)
  const people = PEOPLE_FIELDS[template]

  return (
    <div className="grid min-w-0 grid-cols-1 gap-3">
      {EVENT_FIELDS[template].map((f) => {
        const value = fields[f.key] ?? ''
        const choices = [PAGE_VIEW, ...listed]
        if (value !== '' && !choices.includes(value)) choices.push(value)
        const showPath = f.path !== undefined && (!f.path.viewsOnly || value === PAGE_VIEW)
        return (
          <div key={f.key} className="grid min-w-0 grid-cols-1 gap-3">
            <div className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={`${id}-${f.key}`}>{f.label}</Label>
              <NativeSelect
                id={`${id}-${f.key}`}
                className="max-w-full"
                value={value}
                onChange={(e) => {
                  const next = e.target.value
                  onChange(f.path?.viewsOnly && next !== PAGE_VIEW ? { [f.key]: next, [f.path.key]: '' } : { [f.key]: next })
                }}
              >
                {(f.every !== undefined || value === '') && <NativeSelectOption value="">{f.every ?? 'Choose an event'}</NativeSelectOption>}
                {choices.map((c) => (
                  <NativeSelectOption key={c} value={c}>
                    {c}
                  </NativeSelectOption>
                ))}
              </NativeSelect>
            </div>
            {f.path && showPath && (
              <div className="flex min-w-0 flex-col gap-1.5">
                <Label htmlFor={`${id}-${f.path.key}`}>{f.path.label}</Label>
                <Input
                  id={`${id}-${f.path.key}`}
                  value={fields[f.path.key] ?? ''}
                  placeholder="Every page"
                  onChange={(e) => onChange({ [f.path!.key]: e.target.value })}
                />
              </div>
            )}
          </div>
        )
      })}
      {people && (
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-${people.key}`}>{people.label}</Label>
          <NativeSelect id={`${id}-${people.key}`} value={fields[people.key] || (TEMPLATE_DEFAULTS[template]?.[people.key] ?? '')} onChange={(e) => onChange({ [people.key]: e.target.value })}>
            {people.choices.map(([v, label]) => (
              <NativeSelectOption key={v} value={v}>
                {label}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
      )}
      {error && <p className="text-sm text-destructive">{error}</p>}
    </div>
  )
}
