import { useEffect, useId, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { formatDayTime, rfc3339 } from '@/lib/forms'

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
  name: string
  /** The form's closing time ahead, if one is set. */
  closesAt?: string
  pending?: boolean
  /** Sends update_form's closes_at, an RFC 3339 time; resolves true once saved, which closes the dialog. */
  onStop: (closesAt: string) => Promise<boolean>
}

/**
 * Stop accepting a form's submissions (D12): now, or from a date and time
 * picked in the viewer's time and sent as UTC.
 */
export default function StopDialog({ open, onOpenChange, name, closesAt, pending, onStop }: Props) {
  const id = useId()
  const [at, setAt] = useState('')
  useEffect(() => {
    if (open) setAt('')
  }, [open])
  const picked = at === '' ? null : new Date(at)
  const valid = picked !== null && !Number.isNaN(picked.getTime())

  const stop = async (when: Date) => {
    if (await onStop(rfc3339(when))) onOpenChange(false)
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle className="break-all">Stop accepting {name}</DialogTitle>
          <DialogDescription>
            {closesAt ? `It stops ${formatDayTime(new Date(closesAt))}. ` : ''}
            Submissions after it stops are refused until you reopen it.
          </DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={`${id}-at`}>Stop at</Label>
          <Input id={`${id}-at`} type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} />
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" disabled={pending || !valid} onClick={() => valid && void stop(picked)}>
            Stop at this time
          </Button>
          <Button type="button" disabled={pending} onClick={() => void stop(new Date())}>
            Stop now
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
