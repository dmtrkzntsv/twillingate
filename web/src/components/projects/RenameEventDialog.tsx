import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { EventFamily, EventName } from '@/lib/api'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The event being renamed; kept by the caller through the close animation. */
  target: { family: EventFamily; name: string } | null
  /** The names listed: one the family has is taken, for Merge into…. */
  names: EventName[]
  /** The names the target's family blocks: nothing is renamed onto one. */
  blockedNames: readonly string[]
  pending?: boolean
  /**
   * Resolves true when done, so the dialog closes; false keeps it open (the refusal is already toasted); 'conflict'
   * keeps it open saying the name is taken: the server knows events under it that the listed range does not.
   */
  onRename: (to: string) => Promise<boolean | 'conflict'>
}

/** Type a name not in use yet; one the family has, or blocks, is refused before it is sent, with where to go instead. */
export default function RenameEventDialog({ open, onOpenChange, target, names, blockedNames, pending, onRename }: Props) {
  const [value, setValue] = useState('')
  // The name the server refused as already taken; typing another name clears it.
  const [taken, setTaken] = useState<string | null>(null)
  // Start from nothing each time it opens.
  useEffect(() => {
    if (open) {
      setValue('')
      setTaken(null)
    }
  }, [open])
  const to = value.trim()
  const blocked = target !== null && to !== '' && blockedNames.includes(to)
  const inUse = !blocked && target !== null && to !== '' && (to === taken || names.some((n) => n.family === target.family && n.name === to))
  const valid = target !== null && to !== '' && !to.startsWith('$') && to !== target.name && !blocked && !inUse
  const submit = async () => {
    if (!valid) return
    const out = await onRename(to)
    if (out === 'conflict') setTaken(to)
    else if (out) onOpenChange(false)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <DialogHeader>
            <DialogTitle className="wrap-anywhere">Rename {target?.name}</DialogTitle>
            <DialogDescription className="wrap-anywhere">
              Events arriving as {target?.name} ({target?.family}) are stored under the new name, and the ones already stored are renamed. Widgets whose SQL names the old event are not changed.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor="rename-event-to">New name</Label>
            <Input id="rename-event-to" value={value} autoComplete="off" onChange={(e) => setValue(e.target.value)} />
            {blocked && (
              <p role="alert" className="text-sm wrap-anywhere text-destructive">
                {to} is blocked: nothing is renamed onto a blocked event. To stop receiving {target?.name}, block it.
              </p>
            )}
            {inUse && (
              <p role="alert" className="text-sm wrap-anywhere text-destructive">
                {to} already has events: use Merge into… to fold {target?.name} into it.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button type="submit" disabled={!valid || pending}>Rename</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
