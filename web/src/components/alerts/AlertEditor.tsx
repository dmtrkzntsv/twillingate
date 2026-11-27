import { useId, useState, type FormEvent } from 'react'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { AlertActions } from '@/hooks/use-alert-actions'
import { draftErrors, draftOf, patchOf, templateFieldErrors, type AlertDraft, type DraftEdits } from '@/lib/alert-draft'
import { ApiError, type Alert } from '@/lib/api'
import { toastError } from '@/lib/toast-error'
import ConditionFields from './ConditionFields'
import { errorProps, Field } from './Field'
import TemplateFields from './TemplateFields'

/** A save's warning as the form says it: `Never sent yet: <name>` for an event the project never sent, any other as the server words it. */
function warningText(w: string): string {
  const never = /^`([^`]+)` has never been sent/.exec(w)
  return never ? `Never sent yet: ${never[1]}` : w
}

interface Props {
  projectId: number
  alert: Alert
  /** The page's one set of alert actions, so a check a save asks for is the page's to report. */
  actions: AlertActions
  /** What the inputs hold: the saved alert with the inputs touched over it. The page keeps it, since the backtest previews it too. */
  draft: AlertDraft
  /** Inputs as typed; a template's fields only the keys changed. */
  onEdit: (patch: DraftEdits) => void
  /** Follows the saved alert again (after Discard or a save). */
  onReset: () => void
}

/**
 * **Alert settings**, a live alert's simple edits: its name; a template
 * alert's fields; when it fires (ConditionFields); a broken alert's name
 * alone. Errors show under their input, and Save waits for a change and for
 * none. Save sends only what changed (`patchOf`) and toasts `Saved`; a new
 * definition asks for a check, which the page follows. A refusal (another
 * live alert has the name, or another edit landed first) is said under the
 * form, the edit kept, and the alerts are read again; the events the
 * project never sent come back from a save as `Never sent yet` lines.
 * Discard puts the saved values back. The inputs are held while a save is
 * on its way, since its end puts the saved values back.
 */
export default function AlertEditor({ projectId, alert, actions, draft, onEdit, onReset }: Props) {
  const id = useId()
  const [refusal, setRefusal] = useState<string | null>(null)
  const [warnings, setWarnings] = useState<string[]>([])
  const errors = draftErrors(alert, draft)
  const patch = patchOf(alert, draft)
  const changed = Object.keys(patch).length > 0
  // Inputs that differ from the saved ones in text alone (`3.0` for `3`) save nothing, but Discard still tidies them.
  const edited = changed || JSON.stringify(draft) !== JSON.stringify(draftOf(alert))
  const invalid = Object.keys(errors).length > 0
  const broken = alert.kind === ''

  const save = async (e: FormEvent) => {
    e.preventDefault()
    if (!changed || invalid || actions.pending) return
    setRefusal(null)
    setWarnings([])
    const saved = await actions.update(alert.id, patch, {
      onRefusal: (err) => (err instanceof ApiError ? setRefusal(err.message) : toastError(err)),
    })
    if (!saved) return
    toast.success('Saved')
    setWarnings(saved.warnings ?? [])
    onReset()
  }

  const discard = () => {
    setRefusal(null)
    onReset()
  }

  return (
    <form
      aria-labelledby={`${id}-title`}
      onSubmit={(e) => void save(e)}
      className="grid min-w-0 grid-cols-1 gap-4 sm:max-w-lg [&_[data-slot=native-select-wrapper]]:w-full"
    >
      <h3 id={`${id}-title`} className="text-base font-semibold">
        Alert settings
      </h3>
      {/* Held while a save (or any action) is on its way: what was typed meanwhile would not be sent, and the save's end puts the saved values back. */}
      <fieldset disabled={actions.pending} className="contents">
        <Field id={`${id}-name`} label="Name" error={errors.name}>
          <Input id={`${id}-name`} value={draft.name} onChange={(e) => onEdit({ name: e.target.value })} {...errorProps(`${id}-name`, errors.name)} />
        </Field>
        {!broken && alert.template && (
          <TemplateFields projectId={projectId} template={alert.template} fields={draft.fields} onChange={(fields) => onEdit({ fields })} errors={templateFieldErrors(alert.template, draft.fields)} />
        )}
        {!broken && <ConditionFields alert={alert} draft={draft} errors={errors} onChange={onEdit} />}
      </fieldset>
      {refusal && (
        <p role="alert" className="text-sm break-words text-destructive">
          {refusal}
        </p>
      )}
      {warnings.length > 0 && (
        <ul className="flex flex-col gap-1 text-sm break-words text-amber-600 dark:text-amber-400">
          {warnings.map((w) => (
            <li key={w}>{warningText(w)}</li>
          ))}
        </ul>
      )}
      <div className="flex flex-wrap gap-2">
        <Button type="submit" size="sm" disabled={!changed || invalid || actions.pending}>
          Save
        </Button>
        <Button type="button" variant="outline" size="sm" disabled={!edited || actions.pending} onClick={discard}>
          Discard
        </Button>
      </div>
    </form>
  )
}
