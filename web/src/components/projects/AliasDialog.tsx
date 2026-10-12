import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { ruleCopy, type RuleNoun, type RuleTarget } from './rule-copy'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  noun: RuleNoun
  /** The event or key the alias maps to; kept by the caller through the close animation. */
  target: RuleTarget | null
  /** The names with data listed (the target's family's, for events), to say when the alias has data of its own that merges. */
  taken: readonly string[]
  /** The target's aliases already: adding one again is refused. */
  aliases: readonly string[]
  pending?: boolean
  /** Resolves true when done, so the dialog closes; false keeps it open (the refusal is already toasted). */
  onAdd: (alias: string) => Promise<boolean>
}

/** Type an old name stored as the target from now on; one with data of its own merges, said first. */
export default function AliasDialog({ open, onOpenChange, noun, target, taken, aliases, pending, onAdd }: Props) {
  const copy = ruleCopy(noun)
  const [value, setValue] = useState('')
  // Start from nothing each time it opens.
  useEffect(() => {
    if (open) setValue('')
  }, [open])
  const alias = value.trim()
  const valid = target !== null && alias !== '' && !alias.startsWith('$') && alias !== target.name && !aliases.includes(alias)
  const merging = valid && taken.includes(alias)
  const submit = async () => {
    if (valid && (await onAdd(alias))) onOpenChange(false)
  }
  const id = `add-${noun}-alias`
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
            <DialogTitle className="wrap-anywhere">{target && copy.alias.title(target)}</DialogTitle>
            <DialogDescription className="wrap-anywhere">{target && copy.alias.description(target, alias && valid ? alias : undefined)}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor={id}>{copy.alias.label}</Label>
            <Input id={id} value={value} autoComplete="off" onChange={(e) => setValue(e.target.value)} />
            {merging && target && (
              <p role="alert" className="text-sm wrap-anywhere text-destructive">{copy.alias.merging(alias, target)}</p>
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
