import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ArchiveIcon, ArchiveRestoreIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import LoadError from '@/components/projects/LoadError'
import { useFormActions } from '@/hooks/use-form-actions'
import type { Submission } from '@/lib/api'
import { formatDayTime, formatSource } from '@/lib/forms'
import { submissionQuery } from '@/lib/queries'
import { formatPurgeDate } from '@/lib/time'

interface Props {
  projectId: number
  form: string
  /** The submission shown; undefined keeps the drawer closed. */
  id: string | undefined
  /** RETENTION_ARCHIVED_DAYS: null when never purged, undefined while unknown (no date shown either way). */
  purgeDays?: number | null
  onClose: () => void
}

/** A name and its value, each wrapping anywhere: a long email or URL never widens the drawer. */
function Pairs({ rows, mono = false }: { rows: [string, ReactNode][]; mono?: boolean }) {
  return (
    <dl className="grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-x-3 gap-y-1.5 text-sm">
      {rows.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className={`break-all text-muted-foreground ${mono ? 'font-mono text-xs' : 'text-sm'}`}>{k}</dt>
          <dd className="break-words whitespace-pre-wrap [overflow-wrap:anywhere]">{v === '' ? <span className="text-muted-foreground">—</span> : v}</dd>
        </div>
      ))}
    </dl>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <h3 className="text-sm font-medium">{title}</h3>
      {children}
    </section>
  )
}

/** When an archived submission was archived, and when the daily pass deletes it. */
function archivedLine(archivedAt: string, purgeDays?: number | null): ReactNode {
  const at = new Date(archivedAt)
  const purge = purgeDays ? `; deleted for good on ${formatPurgeDate(new Date(at.getTime() + purgeDays * 86_400_000))} unless restored` : ''
  return (
    <>
      <time dateTime={archivedAt} title={archivedAt}>
        {formatDayTime(at)}
      </time>
      {purge}
    </>
  )
}

function Details({ s, purgeDays }: { s: Submission; purgeDays?: number | null }) {
  const fields = Object.entries(s.fields).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  const arrival: [string, ReactNode][] = [
    ['Received', <time dateTime={s.received_at} title={s.received_at}>{formatDayTime(new Date(s.received_at))}</time>],
    ['Source', formatSource(s.attribution)],
  ]
  if (s.archived_at) arrival.push(['Archived', archivedLine(s.archived_at, purgeDays)])
  return (
    <>
      <Section title="Fields">
        {fields.length === 0 ? <p className="text-sm text-muted-foreground">No fields kept.</p> : <Pairs rows={fields} mono />}
      </Section>
      <Section title="Arrival">
        <Pairs rows={arrival} />
      </Section>
    </>
  )
}

/**
 * One submission (D12a): every stored field, the ones the form no longer
 * expects too, then when it arrived, where its visit came from and, once
 * archived, when; with **Archive** (reversible, so no confirm), or
 * **Restore** on an archived one. Either closes the drawer, since the row
 * leaves the table it was opened from.
 */
export default function SubmissionDrawer({ projectId, form, id, purgeDays, onClose }: Props) {
  const q = useQuery({ ...submissionQuery(projectId, form, id ?? ''), enabled: id !== undefined })
  const actions = useFormActions()
  const archived = q.data?.archived_at !== undefined

  const toggle = async () => {
    if (!id) return
    const body = { ids: [id] }
    const n = await (archived ? actions.restoreSubmissions(projectId, body) : actions.archiveSubmissions(projectId, body))
    if (n !== undefined) onClose()
  }

  return (
    <Sheet open={id !== undefined} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full gap-0 overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Submission</SheetTitle>
          <SheetDescription className="break-all">
            {form} · {id}
          </SheetDescription>
        </SheetHeader>
        <div className="flex flex-col gap-5 px-4 pb-4">
          {q.data ? (
            <Details s={q.data} purgeDays={purgeDays} />
          ) : q.error ? (
            <LoadError what="the submission" error={q.error} onRetry={() => void q.refetch()} />
          ) : (
            <Skeleton aria-hidden className="h-40 w-full" />
          )}
        </div>
        <SheetFooter className="mt-auto">
          <Button type="button" variant="outline" disabled={actions.pending || !q.data} onClick={() => void toggle()}>
            {archived ? <ArchiveRestoreIcon /> : <ArchiveIcon />}
            {archived ? 'Restore' : 'Archive'}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
