import { useEffect, useId, useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import OriginsField from './OriginsField'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  submitLabel: string
  /** Read when the dialog opens, not while it stays open. */
  initial?: { name: string; allowed_origins?: string[] }
  /** Resolves true when saved, so the dialog closes; false keeps it open with the input. */
  onSubmit: (body: { name: string; allowed_origins: string[] }) => Promise<boolean>
  pending?: boolean
}

/** Name and allowed origins (`*` for any): creating a project or editing one. Breakdowns have their own section on the project page. */
export default function ProjectFormDialog({ open, onOpenChange, title, submitLabel, initial, onSubmit, pending }: Props) {
  const nameId = useId()
  const [name, setName] = useState(initial?.name ?? '')
  const [origins, setOrigins] = useState<string[]>(initial?.allowed_origins ?? [])
  // Refill only when the dialog opens: a caller passing a fresh `initial` object each render must not wipe what is being typed.
  useEffect(() => {
    if (open) {
      setName(initial?.name ?? '')
      setOrigins(initial?.allowed_origins ?? [])
    }
  }, [open])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (await onSubmit({ name: name.trim(), allowed_origins: origins.map((o) => o.trim()).filter(Boolean) })) onOpenChange(false)
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
          <DialogFooter>
            <Button type="submit" disabled={pending || name.trim() === ''}>{submitLabel}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
