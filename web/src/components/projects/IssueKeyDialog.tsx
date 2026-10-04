import { useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { IssuedKey } from '@/lib/api'
import IssuedKeyView from './IssuedKeyView'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  onIssue: (label: string) => Promise<IssuedKey | undefined>
  pending?: boolean
}

/** Asks for a label, issues the key, then shows it with its snippet. */
export default function IssueKeyDialog({ open, onOpenChange, onIssue, pending }: Props) {
  const [label, setLabel] = useState('')
  const [issued, setIssued] = useState<IssuedKey | null>(null)
  const close = (o: boolean) => {
    if (!o) {
      setLabel('')
      setIssued(null)
    }
    onOpenChange(o)
  }
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const out = await onIssue(label.trim())
    if (out) setIssued(out)
  }
  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{issued ? 'Key issued' : 'Issue key'}</DialogTitle>
          <DialogDescription>Keys are public identifiers: they ship in page source. Retire one by disabling it.</DialogDescription>
        </DialogHeader>
        {issued ? (
          <IssuedKeyView keyValue={issued.key} snippet={issued.snippet} note={issued.note} />
        ) : (
          <form onSubmit={submit} className="flex flex-col gap-4">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="key-label">Label</Label>
              <Input id="key-label" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="web, ios, staging" required />
            </div>
            <DialogFooter>
              <Button type="submit" disabled={pending || label.trim() === ''}>Issue</Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
