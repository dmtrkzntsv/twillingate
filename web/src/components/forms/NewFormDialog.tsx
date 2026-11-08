import { useEffect, useId, useState, type FormEvent } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router'
import { toast } from 'sonner'
import { Button } from '@/components/ui/button'
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ApiError, endpoints, errorMessage, type Form, type NewForm, type Project } from '@/lib/api'
import { formNameProblem, formPath } from '@/lib/forms'
import { rangeParams } from '@/lib/project-tabs'
import { cn } from '@/lib/utils'
import { FieldPicker } from './ApproveDialog'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  project: Project
}

type FormsData = { forms: Form[]; action_base: string }

/**
 * New form (create_form): a name checked as it is typed, a purpose, the
 * fields to keep (one or more) and a return address. A created form is
 * approved and comes last; the dialog closes and opens its tab, keeping
 * the range. A name the project already has, archived forms included, is
 * said on the name; any other refusal under the fields.
 */
export default function NewFormDialog({ open, onOpenChange, project }: Props) {
  const id = useId()
  const projectId = project.project_id
  const client = useQueryClient()
  const navigate = useNavigate()
  const [url] = useSearchParams()
  const [name, setName] = useState('')
  const [purpose, setPurpose] = useState('')
  const [fields, setFields] = useState<string[]>([])
  const [returnURL, setReturnURL] = useState('')
  const [taken, setTaken] = useState('')
  const [error, setError] = useState('')
  const [pending, setPending] = useState(false)
  // Each opening starts blank.
  useEffect(() => {
    if (!open) return
    setName('')
    setPurpose('')
    setFields([])
    setReturnURL('')
    setTaken('')
    setError('')
  }, [open])

  const problem = formNameProblem(name)
  const nameError = problem || taken
  const ready = !pending && name !== '' && problem === '' && taken === '' && fields.length > 0
  const origins = project.allowed_origins ?? []

  const create = async (e: FormEvent) => {
    e.preventDefault()
    if (!ready) return
    const body: NewForm = { name, expected_fields: fields }
    if (purpose.trim()) body.purpose = purpose.trim()
    if (returnURL.trim()) body.return_url = returnURL.trim()
    setPending(true)
    setTaken('')
    setError('')
    try {
      const made = await endpoints.createForm(projectId, body)
      // Its tab opens at once: the list holds it before the refetch answers.
      client.setQueryData<FormsData>(['forms', projectId, 'active'], (old) =>
        old && !old.forms.some((f) => f.name === made.name) ? { ...old, forms: [...old.forms, made] } : old
      )
      void client.invalidateQueries({ queryKey: ['forms', projectId] })
      void client.invalidateQueries({ queryKey: ['project-activity'] })
      toast.success(`Created ${made.name}`)
      onOpenChange(false)
      void navigate(formPath(projectId, made.name, rangeParams(url).toString()))
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) setTaken(`A form named ${name} already exists (it may be archived).`)
      else setError(errorMessage(err))
    } finally {
      setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>New form</DialogTitle>
          <DialogDescription>
            Created approved: from its first submission each one keeps only the fields below and counts as a conversion.
          </DialogDescription>
        </DialogHeader>
        <form id={`${id}-form`} onSubmit={(e) => void create(e)} className="flex min-w-0 flex-col gap-4">
          <div className="flex min-w-0 flex-col gap-1.5">
            <Label htmlFor={`${id}-name`}>Name</Label>
            <Input
              id={`${id}-name`}
              value={name}
              autoFocus
              autoComplete="off"
              spellCheck={false}
              placeholder="contact"
              className="font-mono"
              aria-invalid={nameError !== '' || undefined}
              aria-describedby={`${id}-name-hint`}
              onChange={(e) => {
                setName(e.target.value)
                setTaken('')
              }}
            />
            <p id={`${id}-name-hint`} className={cn('text-xs break-words', nameError ? 'text-destructive' : 'text-muted-foreground')}>
              {nameError || (
                <>
                  Lower-case letters, digits, _ and -, up to 64; the page posts to <code className="font-mono">/ingest/forms/{name || '<name>'}</code>
                  .
                </>
              )}
            </p>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={`${id}-purpose`}>Purpose</Label>
            <Input id={`${id}-purpose`} value={purpose} onChange={(e) => setPurpose(e.target.value)} placeholder="What the form is for (optional)" />
          </div>
          <div role="group" aria-labelledby={`${id}-fields`} className="flex min-w-0 flex-col gap-2">
            <h3 id={`${id}-fields`} className="text-sm font-medium">
              Fields to keep
            </h3>
            <p className="text-xs text-muted-foreground">
              One or more: the names the form's inputs send, as the table's columns in this order. Uncheck one to take it off.
            </p>
            <FieldPicker fields={[]} checked={fields} onChange={setFields} disabled={pending} />
          </div>
          <div className="flex min-w-0 flex-col gap-1.5">
            <Label htmlFor={`${id}-return`}>Return address</Label>
            <Input
              id={`${id}-return`}
              type="url"
              value={returnURL}
              onChange={(e) => setReturnURL(e.target.value)}
              placeholder="https://example.com/thanks (optional)"
              aria-describedby={`${id}-return-hint`}
            />
            <p id={`${id}-return-hint`} className="text-xs break-words text-muted-foreground">
              Where a plain HTML form sends the visitor back when it sets no <code className="font-mono">$redirect</code>. Its origin must be one of
              the project's allowed origins
              {origins.length > 0 ? `: ${origins.join(', ')}.` : '; this project has none yet (Settings, Domains).'}
            </p>
          </div>
          {error && (
            <p role="alert" className="text-sm break-words text-destructive">
              {error}
            </p>
          )}
        </form>
        <DialogFooter>
          <DialogClose asChild>
            <Button type="button" variant="outline">
              Cancel
            </Button>
          </DialogClose>
          <Button type="submit" form={`${id}-form`} disabled={!ready}>
            Create
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
