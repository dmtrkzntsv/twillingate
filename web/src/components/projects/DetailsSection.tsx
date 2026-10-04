import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import type { Project } from '@/lib/api'
import { receivedAttributesQuery } from '@/lib/queries'
import { describeKey } from './BreakdownsField'
import ProjectFormDialog from './ProjectFormDialog'

interface Props {
  project: Project
  /** The range the page shows: what each breakdown received in it. */
  range: { from: string; to: string }
  onSave: (body: { name: string; allowed_origins: string[]; attributes: string[] }) => Promise<boolean>
  pending?: boolean
}

/** Name, allowed origins and breakdowns (with what each received), with Edit. */
export default function DetailsSection({ project, range, onSave, pending }: Props) {
  const [editing, setEditing] = useState(false)
  const { data } = useQuery(receivedAttributesQuery({ project_id: project.project_id, from: range.from, to: range.to }))
  const received = new Map((data?.keys ?? []).map((r) => [r.key, r]))
  const attributes = project.attributes ?? []
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
        <dt className="text-sm text-muted-foreground">Breakdowns</dt>
        <dd>
          {attributes.length > 0 ? (
            <ul aria-label="Breakdowns" className="flex flex-col gap-0.5">
              {attributes.map((k) => {
                const r = received.get(k)
                return (
                  <li key={k} className="flex items-baseline gap-2">
                    <code className="text-sm">{k}</code>
                    {r && <span className="text-xs text-muted-foreground">{describeKey(r, data?.values_cap ?? 0)}</span>}
                  </li>
                )
              })}
            </ul>
          ) : (
            <span className="text-sm text-muted-foreground">None</span>
          )}
        </dd>
      </dl>
      <ProjectFormDialog
        open={editing}
        onOpenChange={setEditing}
        title={`Edit ${project.name}`}
        projectId={project.project_id}
        submitLabel="Save"
        pending={pending}
        initial={{ name: project.name, allowed_origins: project.allowed_origins, attributes: project.attributes ?? [] }}
        onSubmit={onSave}
      />
    </section>
  )
}
