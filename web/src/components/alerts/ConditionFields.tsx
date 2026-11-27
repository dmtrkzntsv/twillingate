import type { ReactNode } from 'react'
import { useId } from 'react'
import { Input } from '@/components/ui/input'
import { NativeSelect, NativeSelectOption } from '@/components/ui/native-select'
import { draftTakesMinVolume, type AlertDraft, type DraftErrors } from '@/lib/alert-draft'
import { conditionTypes } from '@/lib/alerts'
import type { Alert } from '@/lib/api'
import { errorProps, Field } from './Field'

/** An input with its unit after it (`%`, `days`). */
function WithUnit({ unit, children }: { unit: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-center gap-2">
      {children}
      <span className="shrink-0 text-sm text-muted-foreground">{unit}</span>
    </div>
  )
}

const TYPE_LABELS = { threshold: 'Threshold', change: 'Change', silence: 'Silence' } as const

interface Props {
  alert: Alert
  draft: AlertDraft
  errors: DraftErrors
  onChange: (patch: Partial<AlertDraft>) => void
}

/**
 * When an alert fires, as the editor offers it within the alert's kind: the
 * condition type (not for silence, its only one), then a threshold's
 * comparison and value (a percent for a fraction), a change's direction,
 * percent and what it is compared with, or a silence alert's hours; the
 * window in days (not for silence); and the minimum volume where the server
 * takes one for the condition. Each input's `name` is its draft key.
 */
export default function ConditionFields({ alert, draft, errors, onChange }: Props) {
  const id = useId()
  const types = conditionTypes(alert.kind)
  const fid = (name: string) => `${id}-${name}`
  return (
    <>
      {types.length > 1 && (
        <Field id={fid('type')} label="Condition">
          <NativeSelect id={fid('type')} value={draft.conditionType} onChange={(e) => onChange({ conditionType: e.target.value as AlertDraft['conditionType'] })}>
            {types.map((t) => (
              <NativeSelectOption key={t} value={t}>
                {TYPE_LABELS[t]}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </Field>
      )}
      {draft.conditionType === 'threshold' && (
        <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2">
          <Field id={fid('op')} label="Comparison">
            <NativeSelect id={fid('op')} value={draft.op} onChange={(e) => onChange({ op: e.target.value as AlertDraft['op'] })}>
              <NativeSelectOption value="below">below</NativeSelectOption>
              <NativeSelectOption value="above">above</NativeSelectOption>
            </NativeSelect>
          </Field>
          <Field id={fid('value')} label="Threshold" error={errors.value}>
            {alert.unit === 'fraction' ? (
              <WithUnit unit="%">
                <Input id={fid('value')} name="value" inputMode="decimal" value={draft.value} onChange={(e) => onChange({ value: e.target.value })} {...errorProps(fid('value'), errors.value)} />
              </WithUnit>
            ) : (
              <Input id={fid('value')} name="value" inputMode="decimal" value={draft.value} onChange={(e) => onChange({ value: e.target.value })} {...errorProps(fid('value'), errors.value)} />
            )}
          </Field>
        </div>
      )}
      {draft.conditionType === 'change' && (
        <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2">
          <Field id={fid('direction')} label="Direction">
            <NativeSelect id={fid('direction')} value={draft.direction} onChange={(e) => onChange({ direction: e.target.value as AlertDraft['direction'] })}>
              <NativeSelectOption value="drop">drops</NativeSelectOption>
              <NativeSelectOption value="rise">rises</NativeSelectOption>
              <NativeSelectOption value="either">moves</NativeSelectOption>
            </NativeSelect>
          </Field>
          <Field id={fid('percent')} label="Percent" error={errors.percent}>
            <WithUnit unit="%">
              <Input id={fid('percent')} name="percent" inputMode="decimal" value={draft.percent} onChange={(e) => onChange({ percent: e.target.value })} {...errorProps(fid('percent'), errors.percent)} />
            </WithUnit>
          </Field>
          <div className="sm:col-span-2">
            <Field id={fid('against')} label="Compared with">
              <NativeSelect id={fid('against')} value={draft.against} onChange={(e) => onChange({ against: e.target.value as AlertDraft['against'] })}>
                <NativeSelectOption value="previous">the days before</NativeSelectOption>
                <NativeSelectOption value="week_ago">the same days a week earlier</NativeSelectOption>
              </NativeSelect>
            </Field>
          </div>
        </div>
      )}
      {draft.conditionType === 'silence' && (
        <Field id={fid('hours')} label="Hours without an event" error={errors.hours}>
          <Input id={fid('hours')} name="hours" inputMode="numeric" value={draft.hours} onChange={(e) => onChange({ hours: e.target.value })} {...errorProps(fid('hours'), errors.hours)} />
        </Field>
      )}
      {draft.conditionType !== 'silence' && (
        <Field id={fid('window')} label="Window" error={errors.windowDays}>
          <WithUnit unit="days">
            <Input id={fid('window')} name="windowDays" inputMode="numeric" value={draft.windowDays} onChange={(e) => onChange({ windowDays: e.target.value })} {...errorProps(fid('window'), errors.windowDays)} />
          </WithUnit>
        </Field>
      )}
      {draftTakesMinVolume(alert, draft) && (
        <Field id={fid('volume')} label="Minimum volume" hint="0 for none" error={errors.minVolume}>
          <Input id={fid('volume')} name="minVolume" inputMode="numeric" value={draft.minVolume} onChange={(e) => onChange({ minVolume: e.target.value })} {...errorProps(fid('volume'), errors.minVolume)} />
        </Field>
      )}
    </>
  )
}
