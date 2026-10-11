import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { LayersIcon, LayoutDashboardIcon, PlusIcon } from 'lucide-react'
import { Badge } from '@/components/ui/badge'
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
  /** The dashboard the widget is on, marked "Current" in the list. */
  current: number
  /** Copies onto the dashboard; resolves true when the server took it, so the dialog closes. */
  onCopy: (dashboard: DashboardInfo) => Promise<boolean>
  /**
   * Makes a dashboard with this title, a group of its own or, given
   * `groupId`, that group's last tab, and copies onto it; resolves true
   * when it did, so the dialog closes.
   */
  onCreate: (title: string, groupId?: number) => Promise<boolean>
}

/** Where the title field makes the new dashboard: a group of its own, or a tab of `target`. */
type Creating = { target?: CopyTarget }

/**
 * "Copy to dashboard…": the user's own live dashboards, each a click
 * that copies the widget to its end, one card per group with the
 * widget's own group first and its own dashboard marked "Current". Each card ends with "New tab…", and above
 * them "New dashboard…": either swaps the list for a title field that
 * makes the dashboard and copies there. From a built-in on a fresh
 * install, New dashboard… is the only way.
 */
export default function CopyWidgetDialog({ open, onOpenChange, label, targets, current, onCopy, onCreate }: Props) {
  const [creating, setCreating] = useState<Creating>()
  const [title, setTitle] = useState('')
  // Held from a click or submit until it resolves; a ref, as a second one can land before a re-render.
  const running = useRef(false)
  const [busy, setBusy] = useState(false)
  // Open at the list with an empty field, whichever way it was last closed.
  useEffect(() => {
    if (!open) {
      setCreating(undefined)
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
    if (!tooShort) void run(() => onCreate(title.trim(), creating?.target?.groupId))
  }
  const group = creating?.target?.name
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        {creating ? (
          <form onSubmit={create} className="flex min-w-0 flex-col gap-4">
            <DialogHeader>
              <DialogTitle className="break-words">{group ? `New tab in ${group}` : 'New dashboard'}</DialogTitle>
              <DialogDescription className="break-words">
                {group
                  ? `Makes the last tab of ${group} with a copy of ${label} on it.`
                  : `Makes a dashboard in the sidebar with a copy of ${label} on it.`}
              </DialogDescription>
            </DialogHeader>
            <div className="flex flex-col gap-1.5">
              <Input aria-label={group ? 'Tab title' : 'Dashboard title'} value={title} onChange={(e) => setTitle(e.target.value)} autoFocus />
              <p className="text-xs text-muted-foreground">At least 2 characters</p>
            </div>
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => setCreating(undefined)}>
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
            <Button variant="ghost" className="justify-start" disabled={busy} onClick={() => setCreating({})}>
              <PlusIcon />
              New dashboard…
            </Button>
            {targets.length === 0 ? (
              <p className="text-sm text-muted-foreground">No dashboards of your own yet</p>
            ) : (
              <ul className="flex max-h-[60vh] min-w-0 flex-col gap-2 overflow-y-auto">
                {targets.map((t) => (
                  <Section
                    key={t.groupId}
                    target={t}
                    current={current}
                    busy={busy}
                    onPick={(d) => void run(() => onCopy(d))}
                    onNewTab={() => setCreating({ target: t })}
                  />
                ))}
              </ul>
            )}
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

const rowClass = 'h-auto w-full min-w-0 justify-start rounded-none px-3 py-2 font-normal focus-visible:ring-inset'

interface SectionProps {
  target: CopyTarget
  current: number
  busy: boolean
  onPick: (d: DashboardInfo) => void
  onNewTab: () => void
}

/**
 * One group as a card, as the Archive page and the gallery show groups:
 * a group's name in a shaded header over its tabs, or, for a dashboard
 * alone in its group, its own row on top. Either ends with "New tab…".
 */
function Section({ target, current, busy, onPick, onNewTab }: SectionProps) {
  const id = useId()
  const lone = target.tabs.length === 1 && target.tabs[0].title === target.name
  const newTab = (
    <Button variant="ghost" className={`${rowClass} text-muted-foreground`} disabled={busy} onClick={onNewTab} aria-label={`New tab in ${target.name}…`}>
      <PlusIcon aria-hidden />
      New tab…
    </Button>
  )
  if (lone) {
    const d = target.tabs[0]
    return (
      <li className="flex min-w-0 shrink-0 flex-col divide-y overflow-hidden rounded-lg border">
        <Button variant="ghost" className={rowClass} disabled={busy} onClick={() => onPick(d)}>
          <LayoutDashboardIcon className="text-muted-foreground" aria-hidden />
          <Title dashboard={d} current={current} />
        </Button>
        {newTab}
      </li>
    )
  }
  return (
    <li role="group" aria-labelledby={id} className="flex min-w-0 shrink-0 flex-col overflow-hidden rounded-lg border">
      <div className="flex min-w-0 items-center gap-2 border-b bg-muted/40 px-3 py-2">
        <LayersIcon className="size-4 shrink-0 text-muted-foreground" aria-hidden />
        <h3 id={id} className="truncate text-sm font-medium">
          {target.name}
        </h3>
      </div>
      <div className="flex flex-col divide-y [&>button]:pl-9">
        {target.tabs.map((d) => (
          <Button key={d.dashboard_id} variant="ghost" className={rowClass} disabled={busy} onClick={() => onPick(d)}>
            <Title dashboard={d} current={current} />
          </Button>
        ))}
        {newTab}
      </div>
    </li>
  )
}

/** A row's title, with "Current" on the dashboard the widget is on. */
function Title({ dashboard, current }: { dashboard: DashboardInfo; current: number }) {
  return (
    <>
      <span className="truncate">{dashboard.title}</span>
      {dashboard.dashboard_id === current && (
        <Badge variant="outline" className="ml-auto h-5 shrink-0 py-0 font-normal text-muted-foreground">
          Current
        </Badge>
      )}
    </>
  )
}
