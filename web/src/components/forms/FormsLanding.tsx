import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { PlusIcon } from 'lucide-react'
import { Navigate, useSearchParams } from 'react-router'
import LoadError from '@/components/projects/LoadError'
import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import type { Project } from '@/lib/api'
import { formPath } from '@/lib/forms'
import { readLastForm } from '@/lib/last-form'
import { rangeParams } from '@/lib/project-tabs'
import { formsQuery } from '@/lib/queries'
import FormSnippets from './FormSnippets'
import NewFormDialog from './NewFormDialog'

/**
 * `/projects/:id/forms`: lands, replacing the address, on the form last
 * opened on this project here (`last-form.ts`), else the first in the
 * project's order, keeping the range. With no forms, how to add one: New
 * form, or a page that sends one, which makes it a draft.
 */
export default function FormsLanding({ project }: { project: Project }) {
  const id = project.project_id
  const q = useQuery(formsQuery(id))
  const [url] = useSearchParams()
  const [creating, setCreating] = useState(false)
  const forms = q.data?.forms

  if (q.error && !forms) return <LoadError what="the forms" error={q.error} onRetry={() => void q.refetch()} />
  if (!forms) return <Skeleton aria-hidden className="h-24 w-full" />
  if (forms.length > 0) {
    const last = readLastForm(id)
    const to = forms.find((f) => f.name === last) ?? forms[0]
    return <Navigate to={formPath(id, to.name, rangeParams(url).toString())} replace />
  }
  return (
    <section aria-label="Add a form" className="flex min-w-0 flex-col gap-3 rounded-lg border p-4">
      <h3 className="text-base font-semibold">No forms yet</h3>
      <p className="max-w-prose text-sm text-muted-foreground">Create one with the fields it keeps, then point a page's form at it.</p>
      <Button type="button" className="self-start" onClick={() => setCreating(true)}>
        <PlusIcon />
        New form
      </Button>
      <p className="max-w-prose text-sm text-muted-foreground">
        A form also appears here by itself with its first submission, as a draft that keeps every field for a few days until you approve it.
      </p>
      <FormSnippets projectId={id} actionBase={q.data?.action_base ?? ''} name="contact" />
      <NewFormDialog open={creating} onOpenChange={setCreating} project={project} />
    </section>
  )
}
