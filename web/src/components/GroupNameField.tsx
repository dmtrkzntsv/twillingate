import { useState, type FormEvent } from 'react'
import { CheckIcon, XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { nameTooShort } from '@/lib/arrange'

interface Props {
  name: string
  /** Resolves true when saved, so the field closes; false keeps it open. */
  onRename: (name: string) => Promise<boolean>
  /** Closes the field: after a save, on Escape or ×, or when nothing changed. */
  onDone: () => void
  pending?: boolean
}

/**
 * A sidebar group's name as a field, in place of its entry (group names
 * D9): Enter or ✓ saves, Escape or × leaves it as it was. A name under 2
 * characters, blank included, is never sent: a group's name can be
 * replaced, not cleared (D5).
 */
export default function GroupNameField({ name, onRename, onDone, pending }: Props) {
  const [draft, setDraft] = useState(name)
  const invalid = nameTooShort(draft)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (invalid) return
    const next = draft.trim()
    if (next === name) return onDone()
    if (await onRename(next)) onDone()
  }
  return (
    <form onSubmit={submit} className="flex min-w-0 items-center gap-1 px-1">
      <Input
        aria-label="Group name"
        aria-invalid={invalid}
        autoFocus
        value={draft}
        className="h-7 min-w-0 flex-1 text-sm"
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => e.key === 'Escape' && onDone()}
      />
      <Button type="submit" variant="ghost" size="icon" className="size-7" aria-label="Save group name" disabled={pending || invalid}>
        <CheckIcon />
      </Button>
      <Button type="button" variant="ghost" size="icon" className="size-7" aria-label="Cancel rename" onClick={onDone}>
        <XIcon />
      </Button>
    </form>
  )
}
