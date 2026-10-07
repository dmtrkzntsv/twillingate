import { useId, useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { SearchIcon, XIcon } from 'lucide-react'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import LoadError from '@/components/projects/LoadError'
import { useFormActions } from '@/hooks/use-form-actions'
import type { FoundSubmission } from '@/lib/api'
import { formatDayTime } from '@/lib/forms'
import { findSubmissionsQuery } from '@/lib/queries'

/** The shortest search the server takes. */
const MIN = 2

/** Submissions by form, the forms in the order they first appear (newest first). */
function byForm(subs: FoundSubmission[]): [string, FoundSubmission[]][] {
  const groups = new Map<string, FoundSubmission[]>()
  for (const s of subs) groups.set(s.form, [...(groups.get(s.form) ?? []), s])
  return [...groups]
}

/** A submission's fields on one line, as "name: value", for telling them apart. */
function summary(s: FoundSubmission): string {
  return Object.entries(s.fields)
    .map(([k, v]) => `${k}: ${v}`)
    .join(' · ')
}

/**
 * Find a person (D12): every form's submissions with a field value
 * containing the search, archived forms' too (marked Archived), for an
 * access or erasure request, grouped by form, and **Delete all** behind a
 * confirm, which deletes exactly what the same search finds.
 */
export default function FindPerson({ projectId }: { projectId: number }) {
  const id = useId()
  const [text, setText] = useState('')
  const [search, setSearch] = useState('')
  const [confirming, setConfirming] = useState(false)
  const actions = useFormActions()
  const q = useQuery({ ...findSubmissionsQuery(projectId, search), enabled: search.length >= MIN })
  const found = q.data?.submissions ?? []
  const more = q.data?.next_cursor !== undefined
  const count = `${found.length}${more ? '+' : ''} ${found.length === 1 && !more ? 'submission' : 'submissions'}`

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (text.trim().length >= MIN) setSearch(text.trim())
  }
  const clear = () => {
    setText('')
    setSearch('')
  }
  const remove = async () => {
    const n = await actions.deleteSubmissions(projectId, { search })
    if (n !== undefined) clear()
  }

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <form role="search" onSubmit={submit} className="flex min-w-0 items-center gap-2">
        <Input
          id={`${id}-q`}
          type="search"
          aria-label="Find a person"
          placeholder="Find a person: an email, a name"
          value={text}
          onChange={(e) => setText(e.target.value)}
          className="min-w-0 flex-1 sm:w-72 sm:flex-none"
        />
        <Button type="submit" variant="outline" disabled={text.trim().length < MIN}>
          <SearchIcon />
          Find
        </Button>
      </form>
      {search.length >= MIN && (
        <section aria-label="Found submissions" className="flex flex-col gap-3 rounded-lg border p-3">
          {q.error ? (
            <LoadError what="the submissions" error={q.error} onRetry={() => void q.refetch()} />
          ) : !q.data ? (
            <Skeleton aria-hidden className="h-12 w-full" />
          ) : found.length === 0 ? (
            <div className="flex items-center justify-between gap-2">
              <p className="min-w-0 text-sm break-words text-muted-foreground">No submissions contain “{search}”.</p>
              <Button type="button" variant="ghost" size="icon" aria-label="Clear search" onClick={clear}>
                <XIcon />
              </Button>
            </div>
          ) : (
            <>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <p className="min-w-0 text-sm break-words">
                  {count} {found.length === 1 && !more ? 'contains' : 'contain'} “{search}”
                </p>
                <div className="flex gap-2">
                  <Button type="button" variant="destructive" size="sm" disabled={actions.pending} onClick={() => setConfirming(true)}>
                    Delete all
                  </Button>
                  <Button type="button" variant="ghost" size="icon" className="size-8" aria-label="Clear search" onClick={clear}>
                    <XIcon />
                  </Button>
                </div>
              </div>
              {byForm(found).map(([form, subs]) => (
                <div key={form} className="flex min-w-0 flex-col gap-1">
                  <h3 className="flex min-w-0 flex-wrap items-center gap-2 font-mono text-xs font-medium break-all">
                    {form}
                    {subs[0].archived && (
                      <Badge variant="outline" className="font-sans text-muted-foreground">
                        Archived
                      </Badge>
                    )}
                  </h3>
                  <ul className="flex flex-col gap-1">
                    {subs.map((s) => (
                      <li key={s.id} className="min-w-0 text-sm">
                        <time dateTime={s.received_at} className="mr-2 text-xs text-muted-foreground">
                          {formatDayTime(new Date(s.received_at))}
                        </time>
                        <span className="break-words [overflow-wrap:anywhere]">{summary(s)}</span>
                      </li>
                    ))}
                  </ul>
                </div>
              ))}
            </>
          )}
        </section>
      )}
      <AlertDialog open={confirming} onOpenChange={setConfirming}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle className="break-words">
              Delete {count} containing “{search}”?
            </AlertDialogTitle>
            <AlertDialogDescription>
              Every submission of the project's forms, archived ones included, with a field containing it is deleted for good, with its
              conversion while that is still in the raw window.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void remove()}>Delete submissions</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
