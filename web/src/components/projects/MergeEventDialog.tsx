import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Combobox, ComboboxEmpty, ComboboxInput, ComboboxItem, ComboboxList } from '@/components/ui/combobox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import type { EventFamily, EventName } from '@/lib/api'
import { formatExact } from '@/lib/format'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The event merged away; kept by the caller through the close animation. */
  target: { family: EventFamily; name: string } | null
  /** The events it may merge into: its family's, without itself and the blocked ones. */
  into: EventName[]
  pending?: boolean
  /** Resolves true when done, so the dialog closes; false keeps it open (the refusal is already toasted). */
  onMerge: (to: string) => Promise<boolean>
}

/** Pick the event to fold the target into, said to be final before it is confirmed. */
export default function MergeEventDialog({ open, onOpenChange, target, into, pending, onMerge }: Props) {
  const [to, setTo] = useState<string | null>(null)
  // Start from nothing each time it opens.
  useEffect(() => {
    if (open) setTo(null)
  }, [open])
  const counts = new Map(into.map((e) => [e.name, e.events]))
  const submit = async () => {
    if (to !== null && (await onMerge(to))) onOpenChange(false)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault()
            void submit()
          }}
        >
          <DialogHeader>
            <DialogTitle className="wrap-anywhere">Merge {target?.name} into…</DialogTitle>
            <DialogDescription className="wrap-anywhere">
              {target?.name}&apos;s stored events ({target?.family}) move to the event you pick, and events sent later as {target?.name} are stored
              under it. A merge cannot be undone, and on merged days unique users and groups become an upper bound. Widgets whose SQL names{' '}
              {target?.name} are not changed.
            </DialogDescription>
          </DialogHeader>
          {into.length === 0 ? (
            <p className="text-sm text-muted-foreground">No other {target?.family} events.</p>
          ) : (
            <Combobox items={into.map((e) => e.name)} value={to} onValueChange={(v: string | null) => setTo(v)} inline open>
              <ComboboxInput aria-label="Search events" placeholder="Search events" showTrigger={false} className="h-8" />
              <div className="max-h-56 overflow-y-auto">
                <ComboboxList>
                  {(name: string) => (
                    <ComboboxItem key={name} value={name}>
                      <span className="min-w-0 font-mono text-sm break-all">{name}</span>
                      <span className="ml-auto text-xs text-muted-foreground tabular-nums">{formatExact(counts.get(name) ?? 0)}</span>
                    </ComboboxItem>
                  )}
                </ComboboxList>
              </div>
              <ComboboxEmpty className="flex empty:hidden">No match</ComboboxEmpty>
            </Combobox>
          )}
          <DialogFooter>
            <Button type="submit" variant="destructive" disabled={to === null || pending} className="wrap-anywhere whitespace-normal">
              {to === null ? 'Merge' : `Merge into ${to}`}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
