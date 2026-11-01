import { useEffect, useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { nameTooShort } from '@/lib/arrange'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The dialog's heading, e.g. "Rename tab". */
  title: string
  description: string
  /** The field's label, e.g. "Tab name". */
  label: string
  /** The name the field starts on. */
  name: string
  /**
   * Whether saving the starting name unchanged still sends it: true for an
   * unnamed group, whose field starts on its first tab's title (D5).
   */
  saveUnchanged?: boolean
  /** Resolves true when saved, so the dialog closes; false keeps it open (the refusal is already toasted). */
  onSave: (name: string) => Promise<boolean>
  pending?: boolean
}

/**
 * Renames a dashboard, a tab or a group: the field starts on the current
 * name, and a name under 2 characters, blank included, is never sent (D5,
 * D11). Unchanged text closes without a request unless `saveUnchanged`.
 */
export default function NameDialog({ open, onOpenChange, title, description, label, name, saveUnchanged = false, onSave, pending }: Props) {
  const [draft, setDraft] = useState(name)
  // Start from the current name each time it opens.
  useEffect(() => {
    if (open) setDraft(name)
  }, [open, name])
  const invalid = nameTooShort(draft)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (invalid || pending) return
    const next = draft.trim()
    if (!saveUnchanged && next === name) return onOpenChange(false)
    if (await onSave(next)) onOpenChange(false)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <form className="flex flex-col gap-4" onSubmit={(e) => void submit(e)}>
          <DialogHeader>
            <DialogTitle>{title}</DialogTitle>
            <DialogDescription className="wrap-anywhere">{description}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor="dashboard-name">{label}</Label>
            <Input
              id="dashboard-name"
              autoComplete="off"
              autoFocus
              aria-invalid={invalid}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
            />
            {invalid && <p className="text-xs text-muted-foreground">At least 2 characters.</p>}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={invalid || pending}>
              Save
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
