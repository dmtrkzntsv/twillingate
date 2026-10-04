import { useEffect, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { RadioGroup, RadioGroupItem } from '@/components/ui/radio-group'
import { ToggleGroup, ToggleGroupItem } from '@/components/ui/toggle-group'
import { receivedAttributesQuery } from '@/lib/queries'
import { describeKey, usedLabel } from './breakdowns'
import LoadError from './LoadError'

const DAYS = [7, 14, 30] as const

function dayString(d: Date) {
  return d.toISOString().slice(0, 10)
}

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  projectId: number
  /** The keys the project declares now: not offered again. */
  declared: string[]
  pending?: boolean
  /** Resolves true when saved, so the dialog closes; false keeps it open (the refusal is already toasted). */
  onAdd: (key: string) => Promise<boolean>
}

/** Pick one attribute the project's events carry (7/14/30 days) to break down by. */
export default function AddBreakdownDialog({ open, onOpenChange, projectId, declared, pending, onAdd }: Props) {
  const [days, setDays] = useState<(typeof DAYS)[number]>(30)
  const [selected, setSelected] = useState('')
  // Start from nothing each time it opens.
  useEffect(() => {
    if (open) setSelected('')
  }, [open])
  const to = new Date()
  const from = new Date(to.getTime() - (days - 1) * 86_400_000)
  const { data, error, refetch } = useQuery({
    ...receivedAttributesQuery({ project_id: projectId, from: dayString(from), to: dayString(to) }),
    enabled: open,
    placeholderData: keepPreviousData,
  })
  const candidates = (data?.keys ?? []).filter((r) => r.received && !declared.includes(r.key))
  // The server lists only the busiest received keys; say so when it left some out.
  const receivedListed = data ? data.keys.filter((r) => r.received).length : 0
  const atLimit = data !== undefined && data.breakdowns_max > 0 && data.breakdowns_used >= data.breakdowns_max
  const budgetNote = data ? usedLabel(data.breakdowns_used, data.breakdowns_max) : null
  const add = async () => {
    if (await onAdd(selected)) onOpenChange(false)
  }
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Add breakdown</DialogTitle>
          <DialogDescription>
            Pick an attribute the project's events carry. Each breakdown keeps per-value rows for every event, counted against ATTRIBUTE_BREAKDOWNS_MAX across all projects.
          </DialogDescription>
        </DialogHeader>
        <div className="flex items-center justify-between gap-2">
          <span className="text-sm font-medium">Received in the last</span>
          <ToggleGroup type="single" size="sm" value={String(days)} onValueChange={(v) => v && setDays(Number(v) as (typeof DAYS)[number])}>
            {DAYS.map((d) => <ToggleGroupItem key={d} value={String(d)}>{d} days</ToggleGroupItem>)}
          </ToggleGroup>
        </div>
        {data ? (
          candidates.length > 0 ? (
            <RadioGroup aria-label="Attributes" value={selected} onValueChange={setSelected} className="max-h-64 gap-1 overflow-y-auto rounded-md border p-2">
              {candidates.map((r) => {
                const id = `add-breakdown-${r.key}`
                return (
                  <div key={r.key} className="flex items-center gap-2">
                    <RadioGroupItem id={id} value={r.key} />
                    <Label htmlFor={id} className="flex flex-1 items-baseline justify-between gap-2 font-normal">
                      <code className="text-sm">{r.key}</code>
                      <span className="text-xs text-muted-foreground">{describeKey(r, data.values_cap)}</span>
                    </Label>
                  </div>
                )
              })}
            </RadioGroup>
          ) : (
            <p className="text-sm text-muted-foreground">No new attributes received in this range.</p>
          )
        ) : error ? (
          <LoadError what="received attributes" error={error} onRetry={() => void refetch()} />
        ) : (
          <p className="text-sm text-muted-foreground">Loading…</p>
        )}
        {data && data.keys_total > receivedListed && (
          <p className="text-xs text-muted-foreground">
            Showing the {receivedListed.toLocaleString()} busiest of {data.keys_total.toLocaleString()} keys.
          </p>
        )}
        <DialogFooter className="items-center sm:justify-between">
          <span className="text-xs text-muted-foreground">{budgetNote}</span>
          <Button type="button" disabled={selected === '' || pending || atLimit} title={atLimit ? budgetNote ?? undefined : undefined} onClick={() => void add()}>Add</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
