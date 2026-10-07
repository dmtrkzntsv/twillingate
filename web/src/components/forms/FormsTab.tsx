import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { MoreHorizontalIcon } from 'lucide-react'
import { Link, useSearchParams } from 'react-router'
import CopyButton from '@/components/projects/CopyButton'
import LoadError from '@/components/projects/LoadError'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Skeleton } from '@/components/ui/skeleton'
import { useFormActions } from '@/hooks/use-form-actions'
import type { Form, Project } from '@/lib/api'
import { formatDay, formPath, formStatus, isClosed, rfc3339 } from '@/lib/forms'
import { rangeParams } from '@/lib/project-tabs'
import { formsQuery, keysQuery } from '@/lib/queries'
import ApproveDialog from './ApproveDialog'
import FindPerson from './FindPerson'

const SNIPPET = `<form data-twillingate-form="contact">
  <input name="email" type="email" required>
  <textarea name="message"></textarea>
  <button>Send</button>
</form>`

/** One line of copyable code that wraps rather than widening the page. */
function Code({ value, label }: { value: string; label: string }) {
  return (
    <div className="flex items-start gap-2 rounded-md border p-2">
      <pre className="min-w-0 flex-1 font-mono text-xs break-all whitespace-pre-wrap">{value}</pre>
      <CopyButton value={value} label={label} />
    </div>
  )
}

/**
 * No forms yet: how to add one. A form is created by its first submission,
 * so the hint is a tagged form for a page with the SDK, and the URL a plain
 * HTML form posts to, with one of the project's keys.
 */
function AddFormHint({ projectId }: { projectId: number }) {
  const keysQ = useQuery(keysQuery(projectId))
  const key = keysQ.data?.keys.find((k) => k.state === 'active')?.key ?? 'ak_…'
  const action = `https://<collector>/ingest/forms/contact?key=${key}`
  return (
    <section aria-label="Add a form" className="flex flex-col gap-3 rounded-lg border p-4">
      <h3 className="text-base font-semibold">No forms yet</h3>
      <p className="max-w-prose text-sm text-muted-foreground">
        A form appears here with its first submission, as a draft that keeps every field for a few days. On a page with the twillingate
        script, tag the form:
      </p>
      <Code value={SNIPPET} label="Copy form snippet" />
      <p className="max-w-prose text-sm text-muted-foreground">
        Without the script, point a plain form's <code className="font-mono text-xs">action</code> at the collector, the address your install
        snippet loads <code className="font-mono text-xs">twillingate.js</code> from:
      </p>
      <Code value={action} label="Copy action URL" />
    </section>
  )
}

interface RowProps {
  form: Form
  projectId: number
  search: string
  pending: boolean
  onApprove: () => void
  onStop: () => void
  onArchive: () => void
}

function FormRow({ form, projectId, search, pending, onApprove, onStop, onArchive }: RowProps) {
  const count = `${form.submissions.toLocaleString('en-US')} ${form.submissions === 1 ? 'submission' : 'submissions'}`
  const last = form.last_submitted_at ? ` · last ${formatDay(new Date(form.last_submitted_at))}` : ''
  return (
    <li className="flex min-w-0 items-center gap-2 rounded-lg border p-3">
      <Link to={formPath(projectId, form.name, search)} className="flex min-w-0 flex-1 flex-col gap-1 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
          <span className="font-mono text-sm font-medium break-all">{form.name}</span>
          <Badge variant={form.status === 'draft' ? 'outline' : 'secondary'}>{formStatus(form)}</Badge>
        </span>
        {form.purpose && <span className="text-sm break-words">{form.purpose}</span>}
        <span className="text-xs text-muted-foreground">
          {count}
          {last}
        </span>
      </Link>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" className="size-8 shrink-0" aria-label={`Actions for ${form.name}`}>
            <MoreHorizontalIcon />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {form.status === 'draft' && (
            <DropdownMenuItem disabled={pending} onClick={onApprove}>
              Approve…
            </DropdownMenuItem>
          )}
          {!isClosed(form) && (
            <DropdownMenuItem disabled={pending} onClick={onStop}>
              Stop now
            </DropdownMenuItem>
          )}
          <DropdownMenuItem disabled={pending} onClick={onArchive}>
            Archive
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </li>
  )
}

/**
 * A project's Forms tab (D12): Find a person, then one row per form, drafts
 * first as the API orders them, with its status, purpose, submission count
 * and last submission; a row opens the form's page and its menu approves,
 * stops or archives it. With no forms, a hint on adding one.
 */
export default function FormsTab({ project }: { project: Project }) {
  const id = project.project_id
  const q = useQuery(formsQuery(id))
  const [url] = useSearchParams()
  const search = rangeParams(url).toString()
  const actions = useFormActions()
  const [approving, setApproving] = useState<{ form: Form; open: boolean } | null>(null)
  const forms = q.data?.forms

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <FindPerson projectId={id} />
      {q.error && !forms ? (
        <LoadError what="the forms" error={q.error} onRetry={() => void q.refetch()} />
      ) : !forms ? (
        <Skeleton aria-hidden className="h-24 w-full" />
      ) : forms.length === 0 ? (
        <AddFormHint projectId={id} />
      ) : (
        <ul aria-label="Forms" className="flex flex-col gap-2">
          {forms.map((f) => (
            <FormRow
              key={f.name}
              form={f}
              projectId={id}
              search={search}
              pending={actions.pending}
              onApprove={() => setApproving({ form: f, open: true })}
              onStop={() => void actions.update(id, f.name, { closes_at: rfc3339(new Date()) }, `Stopped ${f.name}`)}
              onArchive={() => void actions.archive(id, f.name)}
            />
          ))}
        </ul>
      )}
      <ApproveDialog
        open={approving?.open === true}
        onOpenChange={(o) => !o && setApproving((a) => a && { ...a, open: false })}
        name={approving?.form.name ?? ''}
        fields={approving?.form.fields ?? []}
        pending={actions.pending}
        onApprove={(fields) => actions.approve(id, approving!.form.name, fields)}
      />
    </div>
  )
}
