import { useEffect, useId, useState, type FormEvent } from 'react'
import ConfirmDialog from '@/components/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import { useClosingTarget } from '@/hooks/use-closing-target'
import type { Project } from '@/lib/api'

const ANY = '*'

interface Props {
  project: Project
  pending?: boolean
  /** Saves the project's whole new origin list; resolves true when saved. */
  onSave: (body: { allowed_origins: string[] }) => Promise<boolean>
}

/**
 * A project's allowed origins, laid out like its attributes: one row each
 * with Remove (confirmed), Add origin, and "Allow everything" (`*`) while
 * the list does not already hold it. Every change saves the whole list.
 */
export default function OriginsSection({ project, pending, onSave }: Props) {
  const origins = project.allowed_origins
  const [adding, setAdding] = useState(false)
  const [allowingAll, setAllowingAll] = useState(false)
  const removing = useClosingTarget<string>()
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
                  <Button variant="ghost" size="sm" aria-label={`Remove ${o}`} disabled={pending} onClick={() => removing.show(o)}>Remove</Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <AddOriginDialog open={adding} onOpenChange={setAdding} existing={origins} pending={pending} onAdd={(o) => save([...origins, o])} />
      <ConfirmDialog
        open={allowingAll}
        onOpenChange={setAllowingAll}
        title="Allow every origin?"
        description={
          <>
            Any website can then send events to {project.name} with its ingest key. The origins listed stay; remove <code>*</code> later to go back to them.
          </>
        }
        action="Allow everything"
        onAction={() => void save([...origins, ANY])}
      />
      <ConfirmDialog
        open={removing.open}
        onOpenChange={removing.onOpenChange}
        title={<>Stop accepting events from {removing.target === ANY ? 'every origin' : removing.target}?</>}
        description={
          removing.target === ANY
            ? 'Only the origins listed can send from a browser.'
            : 'Browsers on that origin can no longer send; events already received stay.'
        }
        action="Remove origin"
        onAction={() => {
          const o = removing.target
          void save(origins.filter((x) => x !== o))
        }}
      />
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
