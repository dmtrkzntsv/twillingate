import { useState, type FormEvent } from 'react'
import { CheckIcon, PencilIcon, XIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'

interface Props {
  name: string
  /** Resolves true when saved, so the field closes; false keeps it open with the input. */
  onRename: (name: string) => Promise<boolean>
  pending?: boolean
}

/**
 * The project page's heading, with a pencil that turns it into a field in
 * place: Enter or ✓ saves, Escape or × leaves it as it was. An unchanged
 * or blank name closes without a request.
 */
export default function ProjectName({ name, onRename, pending }: Props) {
  const [draft, setDraft] = useState<string | null>(null)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    const next = draft?.trim() ?? ''
    if (next === '' || next === name) return setDraft(null)
    if (await onRename(next)) setDraft(null)
  }
  if (draft === null) {
    return (
      <div className="flex min-w-0 items-center gap-1">
        <h1 className="truncate text-xl font-semibold tracking-tight">{name}</h1>
        <Button variant="ghost" size="icon" className="size-8 text-muted-foreground" aria-label="Rename" onClick={() => setDraft(name)}>
          <PencilIcon />
        </Button>
      </div>
    )
  }
  return (
    <form onSubmit={submit} className="flex min-w-0 items-center gap-1">
      <Input
        aria-label="Project name"
        autoFocus
        value={draft}
        className="h-9 w-64 max-w-full text-xl font-semibold tracking-tight md:text-xl"
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => e.key === 'Escape' && setDraft(null)}
      />
      <Button type="submit" variant="ghost" size="icon" className="size-8" aria-label="Save name" disabled={pending}>
        <CheckIcon />
      </Button>
      <Button type="button" variant="ghost" size="icon" className="size-8" aria-label="Cancel rename" onClick={() => setDraft(null)}>
        <XIcon />
      </Button>
    </form>
  )
}
