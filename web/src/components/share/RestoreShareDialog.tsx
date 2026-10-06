import { useEffect, useState, type FormEvent } from 'react'
import { ArchiveAfterSelect } from '@/components/share/ArchiveAfterSelect'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Label } from '@/components/ui/label'
import { useWidgetShareActions } from '@/hooks/use-widget-share-actions'
import type { ArchiveAfter, WidgetShare } from '@/lib/api'
import { DEFAULT_ARCHIVE_AFTER } from '@/lib/share'

interface Props {
  /** The archived share to restore; the dialog is open while there is one. */
  share: WidgetShare | undefined
  onClose: () => void
}

/**
 * Asks when a restored share archives itself again (one month unless
 * changed), then restores it at its old link. A failed restore toasts and
 * leaves the dialog open to try again.
 */
export function RestoreShareDialog({ share, onClose }: Props) {
  const { restore, pending } = useWidgetShareActions()
  const [archiveAfter, setArchiveAfter] = useState<ArchiveAfter>(DEFAULT_ARCHIVE_AFTER)
  // The title outlives `share` while the dialog fades out, instead of going blank.
  const [title, setTitle] = useState('')
  useEffect(() => {
    if (share) setTitle(share.title)
  }, [share])

  const close = () => {
    setArchiveAfter(DEFAULT_ARCHIVE_AFTER)
    onClose()
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!share) return
    if (await restore(share.id, archiveAfter)) close()
  }

  return (
    <Dialog open={share !== undefined} onOpenChange={(o) => !o && close()}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>Restore share</DialogTitle>
          <DialogDescription className="break-words">
            {title} answers at its old link again.
          </DialogDescription>
        </DialogHeader>
        <form onSubmit={submit} className="flex flex-col gap-4">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="restore-share-archive-after">Archive after</Label>
            <ArchiveAfterSelect id="restore-share-archive-after" value={archiveAfter} onChange={setArchiveAfter} />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={close}>
              Cancel
            </Button>
            <Button type="submit" disabled={pending}>
              Restore
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
