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
  /** The event or key being renamed; kept by the caller through the close animation. */
  target: RuleTarget | null
  /** The names with data listed (the target's family's, for events): one of them is taken, for Merge into…. */
  taken: readonly string[]
  /** The names blocked (the target's family's, for events): nothing is renamed onto one. */
  blocked: readonly string[]
  pending?: boolean
  /**
   * Resolves true when done, so the dialog closes; false keeps it open (the refusal is already toasted); 'conflict'
   * keeps it open saying the name is taken: the server knows data under it that the listed range does not.
   */
  onRename: (to: string) => Promise<boolean | 'conflict'>
}

/** Type a name not in use yet; one that has data, or is blocked, is refused before it is sent, with where to go instead. */
export default function RenameDialog({ open, onOpenChange, noun, target, taken, blocked, pending, onRename }: Props) {
  const copy = ruleCopy(noun)
  const [value, setValue] = useState('')
  // The name the server refused as already taken; typing another name clears it.
  const [refused, setRefused] = useState<string | null>(null)
  // Start from nothing each time it opens.
  useEffect(() => {
    if (open) {
      setValue('')
      setRefused(null)
    }
  }, [open])
  const to = value.trim()
  const isBlocked = target !== null && to !== '' && blocked.includes(to)
  const inUse = !isBlocked && target !== null && to !== '' && (to === refused || taken.includes(to))
  const valid = target !== null && to !== '' && !to.startsWith('$') && to !== target.name && !isBlocked && !inUse
  const submit = async () => {
    if (!valid) return
    const out = await onRename(to)
    if (out === 'conflict') setRefused(to)
    else if (out) onOpenChange(false)
  }
  const id = `rename-${noun}-to`
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
            <DialogTitle className="wrap-anywhere">{target && copy.rename.title(target)}</DialogTitle>
            <DialogDescription className="wrap-anywhere">{target && copy.rename.description(target)}</DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-2">
            <Label htmlFor={id}>{copy.rename.label}</Label>
            <Input id={id} value={value} autoComplete="off" onChange={(e) => setValue(e.target.value)} />
            {isBlocked && target && (
              <p role="alert" className="text-sm wrap-anywhere text-destructive">{copy.rename.blocked(to, target)}</p>
            )}
            {inUse && target && (
              <p role="alert" className="text-sm wrap-anywhere text-destructive">{copy.rename.taken(to, target)}</p>
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
