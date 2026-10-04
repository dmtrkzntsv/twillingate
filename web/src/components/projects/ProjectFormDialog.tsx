import { useEffect, useId, useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { CreateProjectBody } from '@/lib/api'
import BreakdownsField from './BreakdownsField'
import OriginsField from './OriginsField'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  /** The project being edited: its received attributes fill the breakdown picker. */
  projectId?: number
  submitLabel: string
  /** Read when the dialog opens, not while it stays open. */
  initial?: CreateProjectBody
  /** Resolves true when saved, so the dialog closes; false keeps it open with the input. */
  onSubmit: (body: Required<CreateProjectBody>) => Promise<boolean>
  pending?: boolean
}

/** Name, allowed origins (`*` for any) and breakdowns: creating a project or editing one. */
export default function ProjectFormDialog({ open, onOpenChange, title, projectId, submitLabel, initial, onSubmit, pending }: Props) {
  const nameId = useId()
  const [name, setName] = useState(initial?.name ?? '')
  const [origins, setOrigins] = useState<string[]>(initial?.allowed_origins ?? [])
  const [attributes, setAttributes] = useState<string[]>(initial?.attributes ?? [])
  // Why Save is disabled, reported by the breakdowns picker (null when the selection fits the budget).
  const [problem, setProblem] = useState<string | null>(null)
  // Refill only when the dialog opens: a caller passing a fresh `initial` object each render must not wipe what is being typed.
  useEffect(() => {
    if (open) {
      setName(initial?.name ?? '')
      setOrigins(initial?.allowed_origins ?? [])
      setAttributes(initial?.attributes ?? [])
      setProblem(null)
    }
  }, [open])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (await onSubmit({ name: name.trim(), allowed_origins: origins.map((o) => o.trim()).filter(Boolean), attributes })) onOpenChange(false)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
          </DialogHeader>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={nameId}>Name</Label>
            <Input id={nameId} value={name} onChange={(e) => setName(e.target.value)} required />
          </div>
          <OriginsField value={origins} onChange={setOrigins} />
          <BreakdownsField projectId={projectId} initial={initial?.attributes ?? []} value={attributes} onChange={setAttributes} onProblem={setProblem} />
          <DialogFooter>
            <Button type="submit" disabled={pending || name.trim() === '' || problem !== null} title={problem ?? undefined}>{submitLabel}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
