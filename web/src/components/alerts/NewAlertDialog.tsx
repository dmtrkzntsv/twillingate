import { useEffect, useId, useRef, useState, type FocusEvent, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { useAlertActions, type AlertActions } from '@/hooks/use-alert-actions'
import { templateFieldErrors, type AlertDraft, type DraftEdits } from '@/lib/alert-draft'
import { blankAlert, blankDraft, createBody, nameError as checkName, stepTwoErrors, suggestName, TEMPLATES, wizardAlert } from '@/lib/alert-wizard'
import { warningsText } from '@/lib/alerts'
import { ApiError, endpoints, errorMessage, type Alert } from '@/lib/api'
import { alertPath, rangeParams } from '@/lib/project-tabs'
import { BacktestAnswer } from './BacktestResult'
import ConditionFields from './ConditionFields'
import { errorProps, Field } from './Field'
import TemplateFields from './TemplateFields'

type Template = NonNullable<Alert['template']>

const STEP_TITLES = { 1: 'Template', 2: 'What and when', 3: 'Backtest and name' } as const

/** The backtest of the alert the wizard would create: preview_alert of its body without the name, keyed on that body, so a changed definition runs again and an unchanged one is read from the cache. */
function Backtest({ projectId, blank, draft }: { projectId: number; blank: Alert; draft: AlertDraft }) {
  const { name: _name, ...body } = createBody(blank, draft)
  const q = useQuery({
    queryKey: ['alert-preview-new', projectId, JSON.stringify(body)],
    queryFn: () => endpoints.previewAlert(projectId, body),
    staleTime: 5 * 60_000,
  })
  return <BacktestAnswer alert={wizardAlert(blank, draft)} query={q} />
}

/**
 * The wizard's steps, mounted only while the dialog is open, so closing it
 * discards what was entered: 1, a template; 2, its fields and when it
 * fires; 3, the backtest and a name. Back keeps what was entered; choosing
 * another template starts from its blank (a typed name kept). `actions` is
 * the dialog's, which keeps itself open while the create runs.
 */
function Wizard({ projectId, actions, onClose }: { projectId: number; actions: AlertActions; onClose: () => void }) {
  const id = useId()
  const navigate = useNavigate()
  const [url] = useSearchParams()
  const [step, setStep] = useState<1 | 2 | 3>(1)
  const [blank, setBlank] = useState<Alert | null>(null)
  const [draft, setDraft] = useState<AlertDraft | null>(null)
  // The name last suggested: the name follows a new suggestion only while it still holds this one (or nothing).
  const [suggested, setSuggested] = useState('')
  const [nameRefusal, setNameRefusal] = useState('')
  const [refusal, setRefusal] = useState('')
  // Step 2's inputs the user changed or left, by draft key or template field key (each input's `name`): only their errors show, so a template opens with none.
  const [touched, setTouched] = useState<ReadonlySet<string>>(new Set())
  // Set from the first click of Create alert until its answer, so a double click sends one create.
  const creating = useRef(false)
  // False once this wizard is gone (the dialog closed), so a create answered after that neither closes nor navigates.
  const mounted = useRef(false)
  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  const choose = (template: Template) => {
    if (blank?.template !== template) {
      const b = blankAlert(projectId, template)
      setBlank(b)
      setDraft((d) => ({ ...blankDraft(b), name: d && d.name !== suggested ? d.name : '' }))
      setSuggested('')
      setTouched(new Set())
    }
    setStep(2)
  }

  const touch = (keys: string[]) => setTouched((t) => (keys.every((k) => t.has(k)) ? t : new Set([...t, ...keys])))

  const edit = (patch: DraftEdits) => {
    touch([...Object.keys(patch).filter((k) => k !== 'fields'), ...Object.keys(patch.fields ?? {})])
    setDraft((d) => d && { ...d, ...patch, fields: { ...d.fields, ...patch.fields } })
  }

  /** The errors of the inputs the user has touched. Next stays off while there are any, and a form whose submit is disabled takes no Enter, so there is no try to show the rest on. */
  const shown = <T extends Record<string, string | undefined>>(errs: T): Partial<T> =>
    Object.fromEntries(Object.entries(errs).filter(([k]) => touched.has(k))) as Partial<T>

  const errors = blank && draft ? stepTwoErrors(blank, draft) : {}
  const ready = Object.keys(errors).length === 0

  const next = (e: FormEvent) => {
    e.preventDefault()
    if (!blank || !draft || !ready) return
    // Only now: the draft is valid, which the suggestion needs.
    const name = suggestName(blank, draft)
    if (draft.name.trim() === '' || draft.name === suggested) edit({ name })
    setSuggested(name)
    setStep(3)
  }

  const back = () => {
    setRefusal('')
    setStep(step === 3 ? 2 : 1)
  }

  const create = async (e: FormEvent) => {
    e.preventDefault()
    if (!blank || !draft || checkName(draft.name) || creating.current) return
    creating.current = true
    setNameRefusal('')
    setRefusal('')
    const made = await actions.create(createBody(blank, draft), {
      // A taken name and the live-alert limit (409) are said under the name; any other refusal under the step.
      onRefusal: (err) => (err instanceof ApiError && err.status === 409 ? setNameRefusal(err.message) : setRefusal(errorMessage(err))),
    })
    if (!mounted.current) return
    if (!made) {
      creating.current = false
      return
    }
    toast.success('Alert created')
    for (const w of warningsText(made.warnings ?? [])) toast(w)
    onClose()
    void navigate(alertPath(projectId, made.id, rangeParams(url).toString()))
  }

  // What is wrong with the name before it is sent (empty, too long), which keeps Create alert off; else the server's refusal of it.
  const nameInvalid = draft ? checkName(draft.name) : undefined
  const nameError = nameInvalid ?? (nameRefusal || undefined)

  return (
    <>
      <DialogHeader>
        <DialogTitle>New alert</DialogTitle>
        <DialogDescription>
          <span>Step {step} of 3</span> · {STEP_TITLES[step]}
        </DialogDescription>
      </DialogHeader>
      {step === 1 && (
        <div role="group" aria-label="Templates" className="grid min-w-0 grid-cols-1 gap-2 sm:grid-cols-2">
          {TEMPLATES.map((t) => (
            <button
              key={t.id}
              type="button"
              aria-pressed={blank?.template === t.id}
              onClick={() => choose(t.id)}
              className="flex min-w-0 flex-col gap-1 rounded-lg border p-3 text-left hover:bg-accent/50 focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none aria-pressed:border-primary aria-pressed:bg-accent/50"
            >
              <span data-title className="font-medium">
                {t.title}
              </span>
              <span className="text-sm text-muted-foreground">{t.description}</span>
            </button>
          ))}
        </div>
      )}
      {step === 2 && blank?.template && draft && (
        <form
          id={`${id}-define`}
          onSubmit={next}
          onBlur={(e: FocusEvent<HTMLFormElement>) => {
            const name = e.target.getAttribute('name')
            if (name) touch([name])
          }}
          className="grid min-w-0 grid-cols-1 gap-4 [&_[data-slot=native-select-wrapper]]:w-full">
          <TemplateFields
            projectId={projectId}
            template={blank.template}
            fields={draft.fields}
            onChange={(fields) => edit({ fields })}
            errors={shown(templateFieldErrors(blank.template, draft.fields))}
          />
          <ConditionFields alert={wizardAlert(blank, draft)} draft={draft} errors={shown(errors)} onChange={edit} />
        </form>
      )}
      {step === 3 && blank && draft && (
        <form id={`${id}-name-form`} onSubmit={(e) => void create(e)} className="grid min-w-0 grid-cols-1 gap-4">
          <Backtest projectId={projectId} blank={blank} draft={draft} />
          <Field id={`${id}-name`} label="Name" error={nameError}>
            <Input
              id={`${id}-name`}
              value={draft.name}
              autoComplete="off"
              onChange={(e) => {
                edit({ name: e.target.value })
                setNameRefusal('')
                setRefusal('')
              }}
              {...errorProps(`${id}-name`, nameError)}
            />
          </Field>
          {refusal && (
            <p role="alert" className="text-sm break-words text-destructive">
              {refusal}
            </p>
          )}
        </form>
      )}
      <DialogFooter>
        {step === 1 ? (
          <DialogClose asChild>
            <Button type="button" variant="outline">
              Cancel
            </Button>
          </DialogClose>
        ) : (
          <Button type="button" variant="outline" disabled={actions.pending} onClick={back}>
            Back
          </Button>
        )}
        {step === 2 && (
          <Button type="submit" form={`${id}-define`} disabled={!ready}>
            Next
          </Button>
        )}
        {step === 3 && (
          <Button type="submit" form={`${id}-name-form`} disabled={actions.pending || nameInvalid !== undefined}>
            Create alert
          </Button>
        )}
      </DialogFooter>
    </>
  )
}

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  projectId: number
}

/**
 * New alert (create_alert), a project's alert made from one of the four
 * templates in three steps (Template, What and when, Backtest and name).
 * Step 3 backtests the definition and suggests a name; Create alert sends
 * it once, then the dialog closes, toasts `Alert created` (and the events
 * the project never sent), and opens the alert's page keeping the range. A
 * taken name or the live-alert limit (409) is said under the name, any other
 * refusal under the step, the dialog staying on step 3 with what was
 * entered. It does not close while the create runs (no Close, Escape or
 * click outside), so its answer always lands in the wizard that sent it. Full
 * width on a phone, scrolling within the screen's height.
 */
export default function NewAlertDialog({ open, onOpenChange, projectId }: Props) {
  const actions = useAlertActions(projectId)
  const hold = (e: Event) => {
    if (actions.pending) e.preventDefault()
  }
  return (
    <Dialog open={open} onOpenChange={(next) => (next || !actions.pending) && onOpenChange(next)}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl" showCloseButton={!actions.pending} onEscapeKeyDown={hold} onInteractOutside={hold}>
        <Wizard projectId={projectId} actions={actions} onClose={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}
