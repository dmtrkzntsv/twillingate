import { useEffect, useId, useState, type FormEvent } from 'react'
import {
  AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription,
  AlertDialogFooter, AlertDialogHeader, AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import type { Project } from '@/lib/api'

const ANY = '*'

interface Props {
  project: Project
  pending?: boolean
  /** Saves the project's whole new origin list; resolves true when saved. */
  onSave: (body: { allowed_origins: string[] }) => Promise<boolean>
}

/**
 * A project's allowed origins, laid out like its breakdowns: one row each
 * with Remove (confirmed), Add origin, and "Allow everything" (`*`) while
 * the list does not already hold it. Every change saves the whole list.
 */
export default function OriginsSection({ project, pending, onSave }: Props) {
  const origins = project.allowed_origins
  const [adding, setAdding] = useState(false)
  const [allowingAll, setAllowingAll] = useState(false)
  // The origin outlives the dialog's close animation, so the title does not flash empty.
  const [removing, setRemoving] = useState<{ origin: string; open: boolean } | null>(null)
  const save = (next: string[]) => onSave({ allowed_origins: next })
  return (
    <section aria-label="Allowed origins" className="flex flex-col gap-3 rounded-lg border p-4">
      <header className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-base font-semibold">Allowed origins</h2>
        <div className="flex items-center gap-2">
          {!origins.includes(ANY) && (
            <Button variant="outline" size="sm" disabled={pending} onClick={() => setAllowingAll(true)}>Allow everything</Button>
          )}
          <Button variant="outline" size="sm" disabled={pending} onClick={() => setAdding(true)}>Add origin</Button>
        </div>
      </header>
      {origins.length === 0 ? (
        <p className="text-sm text-muted-foreground">None: browsers cannot send. Native apps still can.</p>
      ) : (
        <Table aria-label="Allowed origins">
          <TableHeader>
            <TableRow><TableHead>Origin</TableHead><TableHead /></TableRow>
          </TableHeader>
          <TableBody>
            {origins.map((o) => (
              <TableRow key={o}>
                <TableCell className="whitespace-normal">
                  <code className="text-sm break-all">{o}</code>
                  {o === ANY && <span className="text-xs text-muted-foreground"> · any origin</span>}
                </TableCell>
                <TableCell className="text-right">
                  <Button variant="ghost" size="sm" aria-label={`Remove ${o}`} disabled={pending} onClick={() => setRemoving({ origin: o, open: true })}>Remove</Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <AddOriginDialog open={adding} onOpenChange={setAdding} existing={origins} pending={pending} onAdd={(o) => save([...origins, o])} />
      <AlertDialog open={allowingAll} onOpenChange={setAllowingAll}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Allow every origin?</AlertDialogTitle>
            <AlertDialogDescription>
              Any website can then send events to {project.name} with its ingest key. The origins listed stay; remove <code>*</code> later to go back to them.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => void save([...origins, ANY])}>Allow everything</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
      <AlertDialog open={removing?.open === true} onOpenChange={(o) => !o && setRemoving((d) => d && { ...d, open: false })}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Stop accepting events from {removing?.origin === ANY ? 'every origin' : removing?.origin}?</AlertDialogTitle>
            <AlertDialogDescription>
              {removing?.origin === ANY
                ? 'Only the origins listed can send from a browser.'
                : 'Browsers on that origin can no longer send; events already received stay.'}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                const o = removing!.origin
                setRemoving({ origin: o, open: false })
                void save(origins.filter((x) => x !== o))
              }}
            >
              Remove origin
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

interface AddProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The origins listed now: adding one again is refused here. */
  existing: string[]
  pending?: boolean
  /** Resolves true when saved, so the dialog closes; false keeps it open with the input. */
  onAdd: (origin: string) => Promise<boolean>
}

/** One origin to allow, typed: `https://example.com`, a `*` wildcard (`https://*.example.com`), or an app scheme. */
function AddOriginDialog({ open, onOpenChange, existing, pending, onAdd }: AddProps) {
  const id = useId()
  const [value, setValue] = useState('')
  // Start from nothing each time it opens.
  useEffect(() => {
    if (open) setValue('')
  }, [open])
  const origin = value.trim()
  const duplicate = existing.includes(origin)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (await onAdd(origin)) onOpenChange(false)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <form onSubmit={submit} className="flex flex-col gap-4">
          <DialogHeader>
            <DialogTitle>Add origin</DialogTitle>
            <DialogDescription>
              Browsers on this origin can send events. <code>*</code> is a wildcard: <code>https://*.example.com</code> covers every subdomain. Add <code>tauri://localhost</code> or <code>app://.</code> for Tauri or Electron.
            </DialogDescription>
          </DialogHeader>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor={id}>Origin</Label>
            <Input id={id} value={value} placeholder="https://example.com" autoFocus onChange={(e) => setValue(e.target.value)} />
            {duplicate && <p className="text-xs text-muted-foreground">Already allowed.</p>}
          </div>
          <DialogFooter>
            <Button type="submit" disabled={pending || origin === '' || duplicate}>Add</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
