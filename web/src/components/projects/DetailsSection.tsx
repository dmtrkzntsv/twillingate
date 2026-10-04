import { useState } from 'react'
import { Button } from '@/components/ui/button'
import type { Project } from '@/lib/api'
import ProjectFormDialog from './ProjectFormDialog'

interface Props {
  project: Project
  onSave: (body: { name: string; allowed_origins: string[] }) => Promise<boolean>
  pending?: boolean
}

/** Allowed origins, with Edit for the name and origins. */
export default function DetailsSection({ project, onSave, pending }: Props) {
  const [editing, setEditing] = useState(false)
  return (
    <section aria-label="Details" className="flex flex-col gap-3 rounded-lg border p-4">
      <header className="flex items-center justify-between">
        <h2 className="text-base font-semibold">Details</h2>
        <Button variant="outline" size="sm" onClick={() => setEditing(true)}>Edit</Button>
      </header>
      <dl className="grid gap-3 sm:grid-cols-[10rem_1fr]">
        <dt className="text-sm text-muted-foreground">Allowed origins</dt>
        <dd>
          {project.allowed_origins && project.allowed_origins.length > 0 ? (
            <ul aria-label="Allowed origins" className="flex flex-col gap-0.5 text-sm">
              {project.allowed_origins.map((o) => <li key={o}>{o}</li>)}
            </ul>
          ) : (
            <span className="text-sm text-muted-foreground">None: browsers cannot send</span>
          )}
        </dd>
      </dl>
      <ProjectFormDialog
        open={editing}
        onOpenChange={setEditing}
        title={`Edit ${project.name}`}
        submitLabel="Save"
        pending={pending}
        initial={{ name: project.name, allowed_origins: project.allowed_origins }}
        onSubmit={onSave}
      />
    </section>
  )
}
