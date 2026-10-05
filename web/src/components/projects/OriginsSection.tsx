import { useEffect, useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import type { Project } from '@/lib/api'
import OriginsField from './OriginsField'

interface Props {
  project: Project
  /** Resolves true when saved, so the dialog closes. */
  onSave: (body: { allowed_origins: string[] }) => Promise<boolean>
  pending?: boolean
}

/** Allowed origins, one per line, with Edit; the name is renamed in place in the page's heading. */
export default function OriginsSection({ project, onSave, pending }: Props) {
  const [editing, setEditing] = useState(false)
  const [origins, setOrigins] = useState<string[]>(project.allowed_origins)
  // Refill only when the dialog opens, so a refetch does not wipe what is being typed.
  useEffect(() => {
    if (editing) setOrigins(project.allowed_origins)
  }, [editing])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (await onSave({ allowed_origins: origins.map((o) => o.trim()).filter(Boolean) })) setEditing(false)
  }
  return (
    <section aria-label="Allowed origins" className="flex flex-col gap-3 rounded-lg border p-4">
      <header className="flex items-center justify-between">
        <h2 className="text-base font-semibold">Allowed origins</h2>
        <Button variant="outline" size="sm" onClick={() => setEditing(true)}>Edit</Button>
      </header>
      {project.allowed_origins.length > 0 ? (
        <ul aria-label="Allowed origins" className="flex flex-col gap-0.5 text-sm">
          {project.allowed_origins.map((o) => <li key={o} className="break-all">{o}</li>)}
        </ul>
      ) : (
        <span className="text-sm text-muted-foreground">None: browsers cannot send</span>
      )}
      <Dialog open={editing} onOpenChange={setEditing}>
        <DialogContent className="sm:max-w-xl">
          <form onSubmit={submit} className="flex flex-col gap-4">
            <DialogHeader>
              <DialogTitle>Allowed origins of {project.name}</DialogTitle>
            </DialogHeader>
            <OriginsField value={origins} onChange={setOrigins} />
            <DialogFooter>
              <Button type="submit" disabled={pending}>Save</Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </section>
  )
}
