import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { EventFamily, EventName } from '@/lib/api'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The event the alias maps to; kept by the caller through the close animation. */
  target: { family: EventFamily; name: string } | null
  /** The names listed, to say when the alias has events of its own that merge. */
  names: EventName[]
  /** The target's aliases already: adding one again is refused. */
  aliases: readonly string[]
  pending?: boolean
  /** Resolves true when done, so the dialog closes; false keeps it open (the refusal is already toasted). */
  onAdd: (alias: string) => Promise<boolean>
}

/** Type an old name whose events are stored as the target from now on; one with events of its own merges, said first. */
export default function AddAliasDialog({ open, onOpenChange, target, names, aliases, pending, onAdd }: Props) {
  const [value, setValue] = useState('')
  // Start from nothing each time it opens.
  useEffect(() => {
    if (open) setValue('')
  }, [open])
  const alias = value.trim()
  const valid = target !== null && alias !== '' && !alias.startsWith('$') && alias !== target.name && !aliases.includes(alias)
  const merging = valid && names.some((n) => n.family === target!.family && n.name === alias)
  const submit = async () => {
    if (valid && (await onAdd(alias))) onOpenChange(false)
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
            <DialogTitle className="wrap-anywhere">Add an alias to {target?.name}</DialogTitle>
            <DialogDescription className="wrap-anywhere">
              {alias && valid ? `Events sent as ${alias} are stored as ${target?.name}.` : `Events sent under the alias are stored as ${target?.name} (${target?.family}).`}
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor="add-event-alias">Old name</Label>
            <Input id="add-event-alias" value={value} autoComplete="off" onChange={(e) => setValue(e.target.value)} />
            {merging && (
              <p role="alert" className="text-sm wrap-anywhere text-destructive">
                {alias} already has events: they merge into {target?.name}, and this cannot be undone.
              </p>
            )}
          </div>
          <DialogFooter>
            <Button type="submit" disabled={!valid || pending}>Add alias</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
