import { useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import type { Project } from '@/lib/api'
import ProjectFormDialog from './ProjectFormDialog'

interface Props {
  project: Project
  onSave: (body: { name: string; allowed_origins: string[]; attributes: string[] }) => Promise<boolean>
  pending?: boolean
}

/** Name, allowed origins and declared attributes, with Edit. */
export default function DetailsSection({ project, onSave, pending }: Props) {
  const [editing, setEditing] = useState(false)
  const chips = (values: string[] | undefined, none: string) =>
    values && values.length > 0 ? (
      <div className="flex flex-wrap gap-1.5">{values.map((v) => <Badge key={v} variant="secondary">{v}</Badge>)}</div>
    ) : (
      <span className="text-sm text-muted-foreground">{none}</span>
    )
  return (
    <section aria-label="Details" className="flex flex-col gap-3 rounded-lg border p-4">
      <header className="flex items-center justify-between">
        <h2 className="text-base font-semibold">Details</h2>
        <Button variant="outline" size="sm" onClick={() => setEditing(true)}>Edit</Button>
      </header>
      <dl className="grid gap-3 sm:grid-cols-[10rem_1fr]">
        <dt className="text-sm text-muted-foreground">Allowed origins</dt>
        <dd>{chips(project.allowed_origins, 'None: browsers cannot send')}</dd>
        <dt className="text-sm text-muted-foreground">Declared attributes</dt>
        <dd>{chips(project.attributes, 'None')}</dd>
      </dl>
      <ProjectFormDialog
        open={editing}
        onOpenChange={setEditing}
        title={`Edit ${project.name}`}
        submitLabel="Save"
        pending={pending}
        initial={{ name: project.name, allowed_origins: project.allowed_origins, attributes: project.attributes ?? [] }}
        onSubmit={onSave}
      />
    </section>
  )
}
