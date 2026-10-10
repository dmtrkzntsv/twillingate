import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { PlusIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import type { DashboardInfo, ProjectTab } from '@/lib/api'
import { pickerSections } from '@/lib/project-tabs'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** Every dashboard, archived ones included; the picker leaves out what can't be added. */
  dashboards: DashboardInfo[]
  /** The project's tabs now: not offered again. */
  tabs: ProjectTab[]
  pending: boolean
  /** Adds the dashboard as a tab; resolves true when the server took it, so the dialog closes. */
  onAdd: (dashboardId: number) => Promise<boolean>
  /** Makes a dashboard with this title and adds it as a tab; resolves true when it did, so the dialog closes. */
  onCreate: (title: string) => Promise<boolean>
}

/** The least a dashboard title may have; the server enforces it too. */
const MIN_TITLE = 2

/**
 * The "+" picker (project tabs D8): the built-ins not on the project, then
 * the user's live dashboards not on it. Above them, "New dashboard…" swaps
 * the picker for a title field that makes one and adds it (D19, D20).
 */
export default function AddTabDialog({ open, onOpenChange, dashboards, tabs, pending, onAdd, onCreate }: Props) {
  const { builtin, own } = pickerSections(dashboards, tabs)
  const [creating, setCreating] = useState(false)
  const [title, setTitle] = useState('')
  // Held from the submit until the create resolves; a ref, as a second Enter can land before a re-render.
  const submitting = useRef(false)
  const [busy, setBusy] = useState(false)
  // Open at the picker with an empty field, whichever way it was last closed.
  useEffect(() => {
    if (!open) {
      setCreating(false)
      setTitle('')
    }
  }, [open])
  const add = async (id: number) => {
    if (await onAdd(id)) onOpenChange(false)
  }
  const tooShort = title.trim().length < MIN_TITLE
  const create = async (e: FormEvent) => {
    e.preventDefault()
    if (tooShort || pending || submitting.current) return
    submitting.current = true
    setBusy(true)
    try {
      if (await onCreate(title.trim())) onOpenChange(false)
    } finally {
      submitting.current = false
      setBusy(false)
    }
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        {creating ? (
          <form onSubmit={create} className="flex flex-col gap-4">
            <DialogHeader>
              <DialogTitle>New dashboard</DialogTitle>
              <DialogDescription>Makes an empty dashboard and shows it as the last tab of this project.</DialogDescription>
            </DialogHeader>
            <div className="flex flex-col gap-1.5">
              <Input aria-label="Dashboard title" value={title} onChange={(e) => setTitle(e.target.value)} autoFocus />
              <p className="text-xs text-muted-foreground">At least {MIN_TITLE} characters</p>
            </div>
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => setCreating(false)}>
                Back
              </Button>
              <Button type="submit" disabled={pending || busy || tooShort}>
                Create
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>Add tab</DialogTitle>
              <DialogDescription>Show a dashboard as a tab of this project, with the project pinned.</DialogDescription>
            </DialogHeader>
            <Button variant="ghost" className="justify-start" disabled={pending} onClick={() => setCreating(true)}>
              <PlusIcon />
              New dashboard…
            </Button>
            {builtin.length === 0 && own.length === 0 ? (
              <p className="text-sm text-muted-foreground">Every dashboard is already a tab of this project</p>
            ) : (
              <div className="flex max-h-[60vh] min-w-0 flex-col gap-4 overflow-y-auto">
                <Section title="Built-in" dashboards={builtin} pending={pending} onPick={add} />
                <Section title="Your dashboards" dashboards={own} pending={pending} onPick={add} />
              </div>
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

function Section({ title, dashboards, pending, onPick }: { title: string; dashboards: DashboardInfo[]; pending: boolean; onPick: (id: number) => void }) {
  const id = useId()
  if (dashboards.length === 0) return null
  return (
    <div role="group" aria-labelledby={id} className="flex min-w-0 flex-col gap-1">
      <h3 id={id} className="text-xs font-medium text-muted-foreground">{title}</h3>
      {dashboards.map((d) => (
        <Button
          key={d.dashboard_id}
          variant="ghost"
          className="min-w-0 justify-start"
          disabled={pending}
          onClick={() => onPick(d.dashboard_id)}
        >
          <span className="truncate">{d.title}</span>
        </Button>
      ))}
    </div>
  )
}
