import { useState } from 'react'
import ConfirmDialog from '@/components/ConfirmDialog'
import { Button } from '@/components/ui/button'
import { formatPurgeDate } from '@/lib/time'

interface Props {
  name: string
  /** Days an archived project is kept; undefined or 0 keeps it until restored. */
  purgeDays?: number
  pending?: boolean
  onArchive: () => void
}

/** The end of a live project's Settings page: Archive, confirmed. */
export default function DangerSection({ name, purgeDays, pending, onArchive }: Props) {
  const [archiving, setArchiving] = useState(false)
  const purgeOn = purgeDays ? formatPurgeDate(new Date(Date.now() + purgeDays * 86_400_000)) : null
  return (
    <section aria-label="Danger" className="flex flex-col gap-3 rounded-lg border border-destructive/40 p-4">
      <h2 className="text-base font-semibold">Danger</h2>
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="min-w-0 flex-1 basis-64 text-sm text-muted-foreground">Stops ingestion. You can restore the project from the Archive page.</p>
        <Button variant="destructive" size="sm" disabled={pending} onClick={() => setArchiving(true)}>Archive</Button>
      </div>
      <ConfirmDialog
        open={archiving}
        onOpenChange={setArchiving}
        title={<>Archive {name}?</>}
        description={
          <>
            Ingestion stops; data and dashboards keep working.
            {purgeOn ? ` Unless restored, the project and its data are deleted on ${purgeOn} (${purgeDays} days).` : ' It is kept until restored.'}
          </>
        }
        action="Archive project"
        onAction={onArchive}
      />
    </section>
  )
}
