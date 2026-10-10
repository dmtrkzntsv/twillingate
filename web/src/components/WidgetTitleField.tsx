import { useState, type FormEvent } from 'react'
import { CheckIcon, XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

interface Props {
  title: string
  /** Resolves true when saved, so the field closes; false keeps it open with the input. */
  onSave(title: string): Promise<boolean>
  /** The field is done: saved or cancelled. */
  onDone(): void
}

/**
 * A card's title as a field in place (spec D17), the `ProjectName`
 * pattern at a card title's size: Enter or ✓ saves, Escape or × leaves it
 * as it was, and so does leaving it unchanged. A blank or unchanged title
 * closes without a save; clearing a title is an edit in the editor.
 */
export default function WidgetTitleField({ title, onSave, onDone }: Props) {
  const [draft, setDraft] = useState(title)
  const [saving, setSaving] = useState(false)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const next = draft.trim()
    if (next === '' || next === title) return onDone()
    setSaving(true)
    const saved = await onSave(next)
    setSaving(false)
    if (saved) onDone()
  }
  return (
    <form
      onSubmit={submit}
      className="flex min-w-0 flex-1 items-center gap-0.5"
      // Leaving the form, not moving between its input and buttons.
      onBlur={(e) => {
        if (!e.currentTarget.contains(e.relatedTarget) && draft.trim() === title && !saving) onDone()
      }}
    >
      <Input
        aria-label="Widget title"
        autoFocus
        value={draft}
        className="h-7 min-w-0 flex-1 text-sm md:text-sm"
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => e.key === 'Escape' && onDone()}
      />
      <Button type="submit" variant="ghost" size="icon" className="size-7" aria-label="Save title" disabled={saving}>
        <CheckIcon />
      </Button>
      <Button type="button" variant="ghost" size="icon" className="size-7" aria-label="Cancel rename" onClick={onDone}>
        <XIcon />
      </Button>
    </form>
  )
}
