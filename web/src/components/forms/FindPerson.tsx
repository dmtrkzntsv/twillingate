import { useId, useState, type FormEvent } from 'react'
import { useQuery } from '@tanstack/react-query'
import { SearchIcon, XIcon } from 'lucide-react'
import ConfirmDialog from '@/components/ConfirmDialog'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import LoadError from '@/components/projects/LoadError'
import { useFormActions } from '@/hooks/use-form-actions'
import type { FoundSubmission } from '@/lib/api'
import { plural } from '@/lib/format'
import { formatDayTime, purgeDays as purgeDaysOf } from '@/lib/forms'
import { dashboardsQuery, findSubmissionsQuery } from '@/lib/queries'

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
 * containing the search, archived ones and archived forms' too (marked
 * Archived), for an access or erasure request, grouped by form, and
 * **Archive all** behind a confirm, which archives exactly what the same
 * search finds; the daily pass then purges it.
 */
export default function FindPerson({ projectId }: { projectId: number }) {
  const id = useId()
  const [text, setText] = useState('')
  const [search, setSearch] = useState('')
  const [confirming, setConfirming] = useState(false)
  const actions = useFormActions()
  const q = useQuery({ ...findSubmissionsQuery(projectId, search), enabled: search.length >= MIN })
  const purgeDays = purgeDaysOf(useQuery(dashboardsQuery).data)
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
    const n = await actions.archiveSubmissions(projectId, { search })
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
                    Archive all
                  </Button>
                  <Button type="button" variant="ghost" size="icon" className="size-8" aria-label="Clear search" onClick={clear}>
                    <XIcon />
                  </Button>
                </div>
              </div>
              {byForm(found).map(([form, subs]) => (
                <div key={form} className="flex min-w-0 flex-col gap-1">
                  <h3 className="font-mono text-xs font-medium break-all">{form}</h3>
                  <ul className="flex flex-col gap-1">
                    {subs.map((s) => (
                      <li key={s.id} className="min-w-0 text-sm">
                        <time dateTime={s.received_at} className="mr-2 text-xs text-muted-foreground">
                          {formatDayTime(new Date(s.received_at))}
                        </time>
                        {s.archived && (
                          <Badge variant="outline" className="mr-2 text-muted-foreground">
                            Archived
                          </Badge>
                        )}
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
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        titleClassName="break-words"
        title={<>Archive {count} containing “{search}”?</>}
        description={
          <>
            Every submission of the project's forms with a field containing it is archived: it leaves every table at once,{' '}
            {purgeDays === null
              ? 'but it is never deleted: RETENTION_ARCHIVED_DAYS is 0, so archived submissions are kept until restored.'
              : purgeDays === undefined
                ? 'and the daily pass deletes it for good RETENTION_ARCHIVED_DAYS after archiving.'
                : `and the daily pass deletes it for good ${plural(purgeDays, 'day')} after archiving.`}{' '}
            Its conversion stays counted.
          </>
        }
        action="Archive submissions"
        onAction={() => void remove()}
      />
    </div>
  )
}
