import { useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { Skeleton } from '@/components/ui/skeleton'
import LoadError from '@/components/projects/LoadError'
import { useFormActions } from '@/hooks/use-form-actions'
import type { Submission } from '@/lib/api'
import { formatDayTime, formatSource } from '@/lib/forms'
import { submissionQuery } from '@/lib/queries'

interface Props {
  projectId: number
  form: string
  /** The submission shown; undefined keeps the drawer closed. */
  id: string | undefined
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

function Details({ s }: { s: Submission }) {
  const fields = Object.entries(s.fields).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0))
  return (
    <>
      <Section title="Fields">
        {fields.length === 0 ? <p className="text-sm text-muted-foreground">No fields kept.</p> : <Pairs rows={fields} mono />}
      </Section>
      <Section title="Arrival">
        <Pairs
          rows={[
            ['Received', <time dateTime={s.received_at} title={s.received_at}>{formatDayTime(new Date(s.received_at))}</time>],
            ['Source', formatSource(s)],
          ]}
        />
      </Section>
    </>
  )
}

/**
 * One submission (D12a): every stored field, the ones the form no longer
 * expects too, then when it arrived and where its visit came from, and
 * **Delete** behind a confirm.
 */
export default function SubmissionDrawer({ projectId, form, id, onClose }: Props) {
  const q = useQuery({ ...submissionQuery(projectId, form, id ?? ''), enabled: id !== undefined })
  const actions = useFormActions()
  const [confirming, setConfirming] = useState(false)

  const remove = async () => {
    if (!id) return
    const n = await actions.deleteSubmissions(projectId, { ids: [id] })
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
            <Details s={q.data} />
          ) : q.error ? (
            <LoadError what="the submission" error={q.error} onRetry={() => void q.refetch()} />
          ) : (
            <Skeleton aria-hidden className="h-40 w-full" />
          )}
        </div>
        <SheetFooter className="mt-auto">
          <Button type="button" variant="destructive" disabled={actions.pending || !q.data} onClick={() => setConfirming(true)}>
            Delete
          </Button>
        </SheetFooter>
        <AlertDialog open={confirming} onOpenChange={setConfirming}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete this submission?</AlertDialogTitle>
              <AlertDialogDescription>It is gone for good, with its conversion while that is still in the raw window.</AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction onClick={() => void remove()}>Delete submission</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </SheetContent>
    </Sheet>
  )
}
