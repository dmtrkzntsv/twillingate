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
  /** The names listed, to tell a rename from a merge. */
  names: EventName[]
  /** The names the target's family blocks: renaming onto one drops the renamed events, and their history, with it. */
  blockedNames: readonly string[]
  pending?: boolean
  /**
   * Resolves true when done, so the dialog closes; false keeps it open (the refusal is already toasted); 'conflict'
   * keeps it open as a merge confirmation: the server knows events under the new name that the listed range does not.
   */
  onRename: (to: string, merge: boolean) => Promise<boolean | 'conflict'>
}

/**
 * Type the new name of an event; a name the family already has makes it a merge, and a blocked one drops the renamed
 * events, each said before it is confirmed.
 */
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
  const merging = target !== null && to !== '' && (to === taken || names.some((n) => n.family === target.family && n.name === to))
  const intoBlocked = target !== null && to !== '' && blockedNames.includes(to)
  const valid = target !== null && to !== '' && !to.startsWith('$') && to !== target.name
  const submit = async () => {
    if (!valid) return
    const out = await onRename(to, merging || intoBlocked)
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
            <DialogTitle className="break-all">Rename {target?.name}</DialogTitle>
            <DialogDescription>
              Events arriving as {target?.name} ({target?.family}) are stored under the new name, and the ones already stored are renamed. Widgets whose SQL names the old event are not changed.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor="rename-event-to">New name</Label>
            <Input id="rename-event-to" value={value} autoComplete="off" onChange={(e) => setValue(e.target.value)} />
            {intoBlocked && (
              <p role="alert" className="text-sm text-destructive">
                {to} is blocked: {target?.name}&apos;s events are dropped from now on, and the next daily pass deletes {target?.name}&apos;s aggregated history.
              </p>
            )}
            {merging && (
              <p role="alert" className="text-sm text-destructive">
                {to} already has events: this merges {target?.name} into it, and cannot be undone.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button type="submit" disabled={!valid || pending}>{intoBlocked ? `Rename into blocked ${to}` : merging ? `Merge into ${to}` : 'Rename'}</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
