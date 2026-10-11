import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { PlusIcon } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import type { DashboardInfo } from '@/lib/api'
import { nameTooShort, type CopyTarget } from '@/lib/arrange'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The widget's title, for the description. */
  label: string
  /** The user's dashboards it can go to, by group, its own group first (copyTargets). */
  targets: CopyTarget[]
  /** Copies onto the dashboard; resolves true when the server took it, so the dialog closes. */
  onCopy: (dashboard: DashboardInfo) => Promise<boolean>
  /** Makes a dashboard with this title and copies onto it; resolves true when it did, so the dialog closes. */
  onCreate: (title: string) => Promise<boolean>
}

/**
 * "Copy to dashboard…": the user's own live dashboards, each a click
 * that copies the widget to its end, grouped as in the sidebar with the
 * widget's own group first. Above them, "New dashboard…" swaps the list
 * for a title field that makes one and copies there, as the Add tab
 * picker does: from a built-in on a fresh install there is nowhere else.
 */
export default function CopyWidgetDialog({ open, onOpenChange, label, targets, onCopy, onCreate }: Props) {
  const [creating, setCreating] = useState(false)
  const [title, setTitle] = useState('')
  // Held from a click or submit until it resolves; a ref, as a second one can land before a re-render.
  const running = useRef(false)
  const [busy, setBusy] = useState(false)
  // Open at the list with an empty field, whichever way it was last closed.
  useEffect(() => {
    if (!open) {
      setCreating(false)
      setTitle('')
    }
  }, [open])
  const run = async (write: () => Promise<boolean>) => {
    if (running.current) return
    running.current = true
    setBusy(true)
    try {
      if (await write()) onOpenChange(false)
    } finally {
      running.current = false
      setBusy(false)
    }
  }
  const tooShort = nameTooShort(title)
  const create = (e: FormEvent) => {
    e.preventDefault()
    if (!tooShort) void run(() => onCreate(title.trim()))
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        {creating ? (
          <form onSubmit={create} className="flex flex-col gap-4">
            <DialogHeader>
              <DialogTitle>New dashboard</DialogTitle>
              <DialogDescription>Makes a dashboard in the sidebar with a copy of {label} on it.</DialogDescription>
            </DialogHeader>
            <div className="flex flex-col gap-1.5">
              <Input aria-label="Dashboard title" value={title} onChange={(e) => setTitle(e.target.value)} autoFocus />
              <p className="text-xs text-muted-foreground">At least 2 characters</p>
            </div>
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => setCreating(false)}>
                Back
              </Button>
              <Button type="submit" disabled={busy || tooShort}>
                Create
              </Button>
            </DialogFooter>
          </form>
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>Copy to dashboard</DialogTitle>
              <DialogDescription className="break-words">Adds a copy of {label} at the end of one of your dashboards.</DialogDescription>
            </DialogHeader>
            <Button variant="ghost" className="justify-start" disabled={busy} onClick={() => setCreating(true)}>
              <PlusIcon />
              New dashboard…
            </Button>
            {targets.length === 0 ? (
              <p className="text-sm text-muted-foreground">No other dashboards of your own yet</p>
            ) : (
              <div className="flex max-h-[60vh] min-w-0 flex-col gap-4 overflow-y-auto">
                {targets.map((t) => (
                  <Section key={t.groupId} target={t} busy={busy} onPick={(d) => void run(() => onCopy(d))} />
                ))}
              </div>
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

/** One group: its name over its tabs, or just the tab when it is alone and named the same. */
function Section({ target, busy, onPick }: { target: CopyTarget; busy: boolean; onPick: (d: DashboardInfo) => void }) {
  const id = useId()
  const rows = target.tabs.map((d) => (
    <Button key={d.dashboard_id} variant="ghost" className="min-w-0 justify-start" disabled={busy} onClick={() => onPick(d)}>
      <span className="truncate">{d.title}</span>
    </Button>
  ))
  if (target.tabs.length === 1 && target.tabs[0].title === target.name) return <div className="flex min-w-0 flex-col">{rows}</div>
  return (
    <div role="group" aria-labelledby={id} className="flex min-w-0 flex-col gap-1">
      <h3 id={id} className="truncate text-xs font-medium text-muted-foreground">
        {target.name}
      </h3>
      {rows}
    </div>
  )
}
