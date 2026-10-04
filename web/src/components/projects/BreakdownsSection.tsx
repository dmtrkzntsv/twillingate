import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { Project } from '@/lib/api'
import { receivedAttributesQuery } from '@/lib/queries'
import AddBreakdownDialog from './AddBreakdownDialog'
import { describeKey, usedLabel } from './breakdowns'
import LoadError from './LoadError'

interface Props {
  project: Project
  /** The range the page shows: what each breakdown received in it. */
  range: { from: string; to: string }
  pending?: boolean
  /** Saves the project's whole new attribute list; resolves true when saved. */
  onSave: (attributes: string[]) => Promise<boolean>
}

/** A project's breakdowns with what each received: add (picked from what arrived) and remove (confirmed). */
export default function BreakdownsSection({ project, range, pending, onSave }: Props) {
  const [adding, setAdding] = useState(false)
  // The key outlives the dialog's close animation, so the title does not flash "Stop breaking down null?".
  const [removing, setRemoving] = useState<{ key: string; open: boolean } | null>(null)
  const { data, error, refetch } = useQuery(receivedAttributesQuery({ project_id: project.project_id, from: range.from, to: range.to }))
  const received = new Map((data?.keys ?? []).map((r) => [r.key, r]))
  const attributes = project.attributes ?? []
  const atLimit = data !== undefined && data.breakdowns_max > 0 && data.breakdowns_used >= data.breakdowns_max
  return (
    <section aria-label="Breakdowns" className="flex flex-col gap-3 rounded-lg border p-4">
      <header className="flex items-center justify-between">
        <h2 className="text-base font-semibold">
          Breakdowns
          {data && <span className="text-sm font-normal text-muted-foreground"> · {usedLabel(data.breakdowns_used, data.breakdowns_max)}</span>}
        </h2>
        <Button variant="outline" size="sm" disabled={atLimit} title={atLimit ? usedLabel(data.breakdowns_used, data.breakdowns_max) : undefined} onClick={() => setAdding(true)}>Add breakdown</Button>
      </header>
      {attributes.length === 0 ? (
        <p className="text-sm text-muted-foreground">No breakdowns yet. Add one to break product events and measures down by an attribute.</p>
      ) : (
        <Table>
          <TableHeader>
            <TableRow><TableHead>Attribute</TableHead><TableHead>Received</TableHead><TableHead /></TableRow>
          </TableHeader>
          <TableBody>
            {attributes.map((k) => {
              const r = received.get(k)
              return (
                <TableRow key={k}>
                  <TableCell><code className="text-sm">{k}</code></TableCell>
                  <TableCell className="text-xs text-muted-foreground">{r ? describeKey(r, data?.values_cap ?? 0) : ''}</TableCell>
                  <TableCell className="text-right">
                    <Button variant="ghost" size="sm" aria-label={`Remove ${k}`} disabled={pending} onClick={() => setRemoving({ key: k, open: true })}>Remove</Button>
                  </TableCell>
                </TableRow>
              )
            })}
          </TableBody>
        </Table>
      )}
      {error && !data && <LoadError what="received attributes" error={error} onRetry={() => void refetch()} />}
      <AddBreakdownDialog
        open={adding}
        onOpenChange={setAdding}
        projectId={project.project_id}
        declared={attributes}
        pending={pending}
        onAdd={(key) => onSave([...attributes, key])}
      />
      <AlertDialog open={removing?.open === true} onOpenChange={(o) => !o && setRemoving((d) => d && { ...d, open: false })}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Stop breaking down {removing?.key}?</AlertDialogTitle>
            <AlertDialogDescription>
              Days already rolled up keep their rows; days rolled up from now on won't have it, and adding it back later won't fill the gap.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => { const k = removing!.key; setRemoving({ key: k, open: false }); void onSave(attributes.filter((a) => a !== k)) }}>Remove breakdown</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}
