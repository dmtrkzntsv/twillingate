import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Combobox, ComboboxEmpty, ComboboxInput, ComboboxItem, ComboboxList } from '@/components/ui/combobox'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { formatExact } from '@/lib/format'
import { ruleCopy, type RuleNoun, type RuleTarget } from './rule-copy'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  noun: RuleNoun
  /** The event or key merged away; kept by the caller through the close animation. */
  target: RuleTarget | null
  /** What it may merge into, with its count: never itself or a blocked one (and, for events, its family's only). */
  into: { name: string; count: number }[]
  pending?: boolean
  /** Resolves true when done, so the dialog closes; false keeps it open (the refusal is already toasted). */
  onMerge: (to: string) => Promise<boolean>
}

/** Pick what to fold the target into, said to be final before it is confirmed. */
export default function MergeDialog({ open, onOpenChange, noun, target, into, pending, onMerge }: Props) {
  const copy = ruleCopy(noun)
  const [to, setTo] = useState<string | null>(null)
  // Start from nothing each time it opens.
  useEffect(() => {
    if (open) setTo(null)
  }, [open])
  const counts = new Map(into.map((e) => [e.name, e.count]))
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
            <DialogTitle className="wrap-anywhere">{target && copy.merge.title(target)}</DialogTitle>
            <DialogDescription className="wrap-anywhere">{target && copy.merge.description(target)}</DialogDescription>
          </DialogHeader>
          {into.length === 0 ? (
            <p className="text-sm text-muted-foreground">{target && copy.merge.none(target)}</p>
          ) : (
            <Combobox items={into.map((e) => e.name)} value={to} onValueChange={(v: string | null) => setTo(v)} inline open>
              <ComboboxInput aria-label={copy.merge.search} placeholder={copy.merge.search} showTrigger={false} className="h-8" />
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
