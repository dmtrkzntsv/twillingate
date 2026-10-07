import { useId, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { formatDayTime, isClosed, rfc3339 } from '@/lib/forms'

interface Props {
  /** The form's closing time, absent while it is open. */
  closesAt?: string
  pending?: boolean
  /** Sends update_form's closes_at: an RFC 3339 time, or null to reopen. */
  onChange: (closesAt: string | null) => Promise<unknown>
}

/**
 * A form's closing (D12): `Open`, `Closes <date>` or `Closed`; a date and
 * time it closes (picked in the viewer's time, sent as UTC), **Stop now**
 * while it takes submissions, and **Reopen** once it has a closing time.
 */
export default function ClosingControl({ closesAt, pending, onChange }: Props) {
  const id = useId()
  const [at, setAt] = useState('')
  const closed = isClosed({ closes_at: closesAt })
  const status = closesAt === undefined ? 'Open' : closed ? `Closed ${formatDayTime(new Date(closesAt))}` : `Closes ${formatDayTime(new Date(closesAt))}`
  const picked = at === '' ? null : new Date(at)
  const valid = picked !== null && !Number.isNaN(picked.getTime())

  const set = async () => {
    if (!valid) return
    await onChange(rfc3339(picked))
    setAt('')
  }

  return (
    <div role="group" aria-labelledby={`${id}-title`} className="flex flex-col gap-2">
      <div className="flex flex-wrap items-baseline gap-x-2">
        <h3 id={`${id}-title`} className="text-sm font-medium">
          Closing
        </h3>
        <span className="text-sm text-muted-foreground">{status}</span>
      </div>
      <div className="flex flex-wrap items-end gap-2">
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor={`${id}-at`} className="text-xs text-muted-foreground">
            Closes at
          </Label>
          <Input id={`${id}-at`} type="datetime-local" value={at} onChange={(e) => setAt(e.target.value)} className="w-56 max-w-full" />
        </div>
        <Button type="button" variant="outline" disabled={pending || !valid} onClick={() => void set()}>
          Set
        </Button>
        {!closed && (
          <Button type="button" variant="outline" disabled={pending} onClick={() => void onChange(rfc3339(new Date()))}>
            Stop now
          </Button>
        )}
        {closesAt !== undefined && (
          <Button type="button" variant="outline" disabled={pending} onClick={() => void onChange(null)}>
            Reopen
          </Button>
        )}
      </div>
    </div>
  )
}
